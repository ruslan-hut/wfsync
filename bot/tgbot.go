package bot

import (
	"fmt"
	tgbotapi "github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
	"github.com/PaulSonOfLars/gotgbot/v2/ext/handlers"
	"log/slog"
	"strings"
	"time"
	"wfsync/entity"
	"wfsync/lib/sl"
)

type Database interface {
	GetTelegramUsers() ([]*entity.User, error)
	SetTelegramEnabled(id int64, isActive bool, logLevel int) error
}

type TgBot struct {
	log         *slog.Logger
	api         *tgbotapi.Bot
	db          Database
	users       map[int64]*entity.User
	minLogLevel slog.Level
}

func NewTgBot(apiKey string, db Database, log *slog.Logger) (*TgBot, error) {
	tgBot := &TgBot{
		log:         log.With(sl.Module("tgbot")),
		db:          db,
		minLogLevel: slog.LevelDebug,
		users:       make(map[int64]*entity.User),
	}

	api, err := tgbotapi.NewBot(apiKey, nil)
	if err != nil {
		return nil, fmt.Errorf("creating api instance: %v", err)
	}
	tgBot.api = api

	return tgBot, nil
}

func (t *TgBot) Start() error {
	t.loadUsers()

	dispatcher := ext.NewDispatcher(&ext.DispatcherOpts{
		// If an error is returned by a handler, log it and continue going.
		Error: func(b *tgbotapi.Bot, ctx *ext.Context, err error) ext.DispatcherAction {
			t.log.Error("handling update:", sl.Err(err))
			return ext.DispatcherActionNoop
		},
		MaxRoutines: ext.DefaultMaxRoutines,
	})
	updater := ext.NewUpdater(dispatcher, nil)

	dispatcher.AddHandler(handlers.NewCommand("start", t.start))
	dispatcher.AddHandler(handlers.NewCommand("stop", t.stop))
	dispatcher.AddHandler(handlers.NewCommand("level", t.level))

	// Start receiving updates.
	err := updater.StartPolling(t.api, &ext.PollingOpts{
		DropPendingUpdates: true,
		GetUpdatesOpts: &tgbotapi.GetUpdatesOpts{
			Timeout: 9,
			RequestOpts: &tgbotapi.RequestOpts{
				Timeout: time.Second * 10,
			},
		},
	})
	if err != nil {
		panic("failed to start polling: " + err.Error())
	}

	// Idle, to keep updates coming in, and avoid bot stopping.
	updater.Idle()

	// Set up an update configuration
	return nil
}

func (t *TgBot) loadUsers() {
	if t.db == nil {
		return
	}
	users, err := t.db.GetTelegramUsers()
	if err != nil {
		t.log.Error("loading users", sl.Err(err))
		return
	}
	t.users = make(map[int64]*entity.User)
	active := 0
	for _, user := range users {
		t.users[user.TelegramId] = user
		if user.TelegramEnabled {
			active++
		}
	}
	t.log.With(
		slog.Int("count", len(t.users)),
		slog.Int("active", active),
	).Debug("loaded users")
}

func (t *TgBot) findUser(id int64) *entity.User {
	user, ok := t.users[id]
	if !ok {
		return user
	}
	return nil
}

func (t *TgBot) start(_ *tgbotapi.Bot, ctx *ext.Context) error {
	if t.db == nil {
		return nil
	}
	user := t.findUser(ctx.EffectiveUser.Id)
	if user == nil {
		return nil
	}

	err := t.db.SetTelegramEnabled(user.TelegramId, true, int(t.minLogLevel))
	if err != nil {
		t.plainResponse(user.TelegramId, "Error setting Telegram enabled: "+err.Error())
		return nil
	}
	t.plainResponse(user.TelegramId, "Status changed to ENABLED")
	t.loadUsers()
	return nil
}

func (t *TgBot) stop(_ *tgbotapi.Bot, ctx *ext.Context) error {
	if t.db == nil {
		return nil
	}
	user := t.findUser(ctx.EffectiveUser.Id)
	if user == nil {
		return nil
	}

	err := t.db.SetTelegramEnabled(user.TelegramId, false, int(t.minLogLevel))
	if err != nil {
		t.plainResponse(user.TelegramId, "Error setting Telegram disabled: "+err.Error())
		return nil
	}
	t.plainResponse(user.TelegramId, "Status changed to DISABLED")
	t.loadUsers()
	return nil
}

// level handles the /level command to set the minimum log level for admin notifications
func (t *TgBot) level(_ *tgbotapi.Bot, ctx *ext.Context) error {
	if t.db == nil {
		return nil
	}
	user := t.findUser(ctx.EffectiveUser.Id)
	if user == nil {
		return nil
	}

	// Get the level argument
	args := strings.Fields(ctx.EffectiveMessage.Text)
	if len(args) < 2 {
		currentLevel := slog.Level(user.LogLevel).String()
		t.plainResponse(user.TelegramId, fmt.Sprintf("Your current log level: %s\nAvailable levels: debug, info, warn, error", currentLevel))
		return nil
	}

	// Parse the level
	levelStr := strings.ToLower(args[1])
	level := t.minLogLevel
	switch levelStr {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		t.plainResponse(user.TelegramId, fmt.Sprintf("Invalid level: %s\nAvailable levels: debug, info, warn, error", levelStr))
		return nil
	}

	err := t.db.SetTelegramEnabled(user.TelegramId, true, int(level))
	if err != nil {
		t.plainResponse(user.TelegramId, "Error setting level: "+err.Error())
		return nil
	}
	t.plainResponse(user.TelegramId, fmt.Sprintf("Log level set to: %s", level.String()))
	t.loadUsers()
	return nil
}

func (t *TgBot) SendMessage(msg string) {
	t.SendMessageWithLevel(msg, t.minLogLevel)
}

// SendMessageWithLevel sends a message to all admins with the specified log level
func (t *TgBot) SendMessageWithLevel(msg string, level slog.Level) {
	l := int(level)
	for _, user := range t.users {
		if !user.TelegramEnabled {
			continue
		}
		if l >= user.LogLevel {
			t.plainResponse(user.TelegramId, msg)
		}
	}
}

func (t *TgBot) plainResponse(chatId int64, text string) {

	text = strings.ReplaceAll(text, "**", "*")
	text = strings.ReplaceAll(text, "![", "[")

	sanitized := sanitize(text, false)

	if sanitized != "" {
		_, err := t.api.SendMessage(chatId, sanitized, &tgbotapi.SendMessageOpts{
			ParseMode: "MarkdownV2",
		})
		if err != nil {
			t.log.With(
				slog.Int64("id", chatId),
			).Warn("sending message", sl.Err(err))
			_, err = t.api.SendMessage(chatId, sanitized, &tgbotapi.SendMessageOpts{})
			if err != nil {
				t.log.With(
					slog.Int64("id", chatId),
				).Error("sending safe message", sl.Err(err))
			}
		}
	} else {
		t.log.With(
			slog.Int64("id", chatId),
		).Debug("empty message")
	}
}

func sanitize(input string, preserveLinks bool) string {
	// Define a list of reserved characters that need to be escaped
	reservedChars := "\\`_{}#+-.!|()[]"
	if preserveLinks {
		reservedChars = "\\`_{}#+-.!|"
	}

	// Loop through each character in the input string
	sanitized := ""
	for _, char := range input {
		// Check if the character is reserved
		if strings.ContainsRune(reservedChars, char) {
			// Escape the character with a backslash
			sanitized += "\\" + string(char)
		} else {
			// Add the character to the sanitized string
			sanitized += string(char)
		}
	}

	return sanitized
}
