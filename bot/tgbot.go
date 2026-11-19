// Package bot implements a Telegram bot for managing user notifications.
//
// Architecture overview:
//   - tgbot.go    — TgBot struct, lifecycle (Start/Stop), user cache, Database interface
//   - commands.go  — User-facing commands: /start, /stop, /level, /topics, /tier, /status, /help
//   - admin.go     — Admin commands: /users, /approve, /revoke, /admin, /invite
//   - callbacks.go — Inline keyboard builders and callback query handlers
//   - menus.go     — Per-user command menus via Telegram's BotCommandScope API
//   - messaging.go — Notification routing: level filter → topic filter → tier dispatch
//   - digest.go    — DigestBuffer for batched notification delivery
//   - helpers.go   — Shared utilities: Sanitize, plainResponse, resolveUser, reportError
//
// Data flow for incoming notifications (e.g., from slog handler):
//
//	SendMessageWithTopic → for each user: check enabled/approved/level/topic → route by tier:
//	  realtime → immediate send
//	  critical → immediate send only if level >= ERROR
//	  digest   → buffer in DigestBuffer, flushed on interval
//
// Thread safety: the users map and adminIds are protected by sync.RWMutex.
// All commands and callbacks acquire RLock to read; loadUsers() acquires full Lock to refresh.
package bot

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
	"wfsync/entity"
	"wfsync/lib/sl"

	tgbotapi "github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
	"github.com/PaulSonOfLars/gotgbot/v2/ext/handlers"
	"github.com/PaulSonOfLars/gotgbot/v2/ext/handlers/filters/callbackquery"
)

// BotConfig holds Telegram-specific configuration loaded from the YAML config file.
type BotConfig struct {
	RequireApproval   bool
	DigestIntervalMin int
	DefaultTier       string
	InviteCodeLength  int
}

// Database defines the storage operations the bot depends on.
// Implemented by internal/database/mongo.go.
type Database interface {
	GetTelegramUsers() ([]*entity.User, error)
	GetAllTelegramUsers() ([]*entity.User, error)
	GetTelegramUserById(telegramId int64) (*entity.User, error)
	SetTelegramEnabled(id int64, isActive bool, logLevel int) error
	RegisterTelegramUser(telegramId int64, username string) error
	SetTelegramRole(telegramId int64, role entity.TelegramRole) error
	GetPendingTelegramUsers() ([]*entity.User, error)
	SetTelegramTopics(telegramId int64, topics []string) error
	SetSubscriptionTier(telegramId int64, tier entity.SubscriptionTier, schedule string) error
	CreateInviteCode(code *entity.InviteCode) error
	UseInviteCode(code string, telegramId int64) error
	MigrateExistingTelegramUsers() error
	GetAllPendingRetryJobs() ([]*entity.RetryJob, error)
}

// RetryRunner triggers an immediate retry of an order's invoice registration,
// bypassing the queue's backoff schedule. Implemented by core.RetryQueue.
type RetryRunner interface {
	RetryNow(orderId string) (*entity.RetryJob, error)
}

// TgBot is the central Telegram bot instance.
// It caches all users in memory (refreshed on every state change) and routes
// notifications through the level → topic → tier pipeline.
type TgBot struct {
	log         *slog.Logger
	api         *tgbotapi.Bot
	token       string
	db          Database
	mu          sync.RWMutex           // guards users, adminIds and retryQueue
	users       map[int64]*entity.User // telegram_id → User; includes all roles
	minLogLevel slog.Level
	updater     *ext.Updater
	digest      *DigestBuffer
	adminIds    []int64 // cached admin telegram IDs for quick notification
	config      BotConfig
	// retryQueue is wired after the bot is already polling (the queue is built later in
	// startup), so it is guarded by mu like the rest of the mutable state, and may
	// legitimately be nil — both while starting up and when the queue is disabled.
	retryQueue RetryRunner
}

// SetRetryQueue wires the retry runner behind the /retry command. Safe to call while
// the bot is running; leaving it unset simply disables the command.
func (t *TgBot) SetRetryQueue(r RetryRunner) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.retryQueue = r
}

// retryRunner returns the configured runner, or nil when none is wired.
func (t *TgBot) retryRunner() RetryRunner {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.retryQueue
}

func NewTgBot(apiKey string, db Database, log *slog.Logger, cfg BotConfig) (*TgBot, error) {
	if cfg.InviteCodeLength == 0 {
		cfg.InviteCodeLength = 8
	}
	if cfg.DigestIntervalMin == 0 {
		cfg.DigestIntervalMin = 60
	}

	tgBot := &TgBot{
		log:         log.With(sl.Module("tgbot")),
		db:          db,
		minLogLevel: slog.LevelDebug,
		users:       make(map[int64]*entity.User),
		config:      cfg,
	}

	tgBot.token = apiKey

	api, err := tgbotapi.NewBot(apiKey, &tgbotapi.BotOpts{
		BotClient: &tgbotapi.BaseBotClient{Client: newHTTPClient()},
	})
	if err != nil {
		return nil, fmt.Errorf("creating api instance: %s", tgBot.redact(err))
	}
	tgBot.api = api

	return tgBot, nil
}

func (t *TgBot) Start() error {
	t.loadUsers()
	t.sanitizeUserTopics()

	// Start digest buffer
	interval := time.Duration(t.config.DigestIntervalMin) * time.Minute
	t.digest = NewDigestBuffer(t, interval)
	t.digest.StartTicker()

	dispatcher := ext.NewDispatcher(&ext.DispatcherOpts{
		Error: func(b *tgbotapi.Bot, ctx *ext.Context, err error) ext.DispatcherAction {
			t.log.Error("handling update", t.errAttr(err))
			return ext.DispatcherActionNoop
		},
		MaxRoutines: ext.DefaultMaxRoutines,
	})
	t.updater = ext.NewUpdater(dispatcher, &ext.UpdaterOpts{
		// The default handler prints the request URL, which carries the bot token.
		// It also sleeps 1s before the next poll, so keep that pause.
		UnhandledErrFunc: func(err error) {
			t.log.Warn("get updates", t.errAttr(err))
			time.Sleep(time.Second)
		},
	})

	// User commands
	dispatcher.AddHandler(handlers.NewCommand("start", t.start))
	dispatcher.AddHandler(handlers.NewCommand("stop", t.stop))
	dispatcher.AddHandler(handlers.NewCommand("level", t.level))
	dispatcher.AddHandler(handlers.NewCommand("topics", t.topics))
	dispatcher.AddHandler(handlers.NewCommand("subscribe", t.subscribe))
	dispatcher.AddHandler(handlers.NewCommand("unsubscribe", t.unsubscribe))
	dispatcher.AddHandler(handlers.NewCommand("tier", t.tier))
	dispatcher.AddHandler(handlers.NewCommand("status", t.status))
	dispatcher.AddHandler(handlers.NewCommand("help", t.help))

	// Admin commands
	dispatcher.AddHandler(handlers.NewCommand("users", t.usersCmd))
	dispatcher.AddHandler(handlers.NewCommand("approve", t.approve))
	dispatcher.AddHandler(handlers.NewCommand("revoke", t.revoke))
	dispatcher.AddHandler(handlers.NewCommand("admin", t.adminCmd))
	dispatcher.AddHandler(handlers.NewCommand("invite", t.invite))
	dispatcher.AddHandler(handlers.NewCommand("retries", t.retries))
	dispatcher.AddHandler(handlers.NewCommand("retry", t.retry))

	// Callback query handlers
	dispatcher.AddHandler(handlers.NewCallback(callbackquery.Prefix(cbTopicToggle), t.onTopicCallback))
	dispatcher.AddHandler(handlers.NewCallback(callbackquery.Prefix(cbTier), t.onTierCallback))
	dispatcher.AddHandler(handlers.NewCallback(callbackquery.Prefix(cbLevel), t.onLevelCallback))
	dispatcher.AddHandler(handlers.NewCallback(callbackquery.Prefix(cbApprove), t.onApproveCallback))
	dispatcher.AddHandler(handlers.NewCallback(callbackquery.Prefix(cbRevoke), t.onRevokeCallback))

	// Set default bot command menu and sync per-user menus
	t.setDefaultCommands()
	t.syncAllUserMenus()

	err := t.updater.StartPolling(t.api, &ext.PollingOpts{
		DropPendingUpdates: true,
		GetUpdatesOpts: &tgbotapi.GetUpdatesOpts{
			Timeout: 9,
			RequestOpts: &tgbotapi.RequestOpts{
				Timeout: time.Second * 10,
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to start polling: %s", t.redact(err))
	}

	t.updater.Idle()
	return nil
}

// newHTTPClient returns the client for Telegram API calls. The default transport never
// health-checks an HTTP/2 connection: a request timeout cancels the stream but keeps the
// connection, so after a silent drop getUpdates kept timing out for 30–45 minutes on the
// same dead connection. With pings it is closed within ~40 s and the next poll redials.
func newHTTPClient() http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.HTTP2 = &http.HTTP2Config{
		SendPingTimeout: 30 * time.Second,
		PingTimeout:     10 * time.Second,
	}
	return http.Client{Transport: tr}
}

// redact returns the error text with the bot token masked: transport errors quote the
// request URL, which carries the token.
func (t *TgBot) redact(err error) string {
	if t.token == "" {
		return err.Error()
	}
	return strings.ReplaceAll(err.Error(), t.token, "***")
}

// errAttr is sl.Err for errors that may come from a Telegram API call.
func (t *TgBot) errAttr(err error) slog.Attr {
	return slog.String("error", t.redact(err))
}

func (t *TgBot) Stop() {
	if t.digest != nil {
		t.digest.Stop()
	}
	if t.updater != nil {
		t.log.Info("stopping telegram bot")
		t.updater.Stop()
	}
}

// loadUsers refreshes the in-memory user cache from the database.
// Called on startup and after every state-changing operation (approve, topic change, etc.).
// Rebuilds the adminIds list used by notifyAdmins.
func (t *TgBot) loadUsers() {
	if t.db == nil {
		return
	}
	users, err := t.db.GetAllTelegramUsers()
	if err != nil {
		t.log.Error("loading users", sl.Err(err))
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	t.users = make(map[int64]*entity.User)
	t.adminIds = nil
	active := 0
	for _, user := range users {
		t.users[user.TelegramId] = user
		if user.TelegramEnabled {
			active++
		}
		if user.IsAdmin() {
			t.adminIds = append(t.adminIds, user.TelegramId)
		}
	}
	t.log.With(
		slog.Int("count", len(t.users)),
		slog.Int("active", active),
		slog.Int("admins", len(t.adminIds)),
	).Debug("loaded users")
}

func (t *TgBot) findUser(id int64) *entity.User {
	t.mu.RLock()
	defer t.mu.RUnlock()
	user, ok := t.users[id]
	if ok {
		return user
	}
	return nil
}
