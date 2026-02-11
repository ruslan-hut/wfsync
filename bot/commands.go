package bot

import (
	"fmt"
	"log/slog"
	"strings"
	"wfsync/entity"

	tgbotapi "github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
)

// start handles the /start command. Three cases:
//  1. Known approved user → re-enable notifications
//  2. Known pending user → inform about awaiting approval
//  3. Unknown user → register; auto-approve if valid invite code or approval not required,
//     otherwise mark as pending and notify admins with approve/revoke buttons.
//
// Invite codes are passed via Telegram deep links: /start CODE
func (t *TgBot) start(_ *tgbotapi.Bot, ctx *ext.Context) error {
	if t.db == nil {
		return nil
	}
	chatId := ctx.EffectiveUser.Id
	user := t.findUser(chatId)

	// Case 1: Known approved user — re-enable
	if user != nil && user.IsApproved() {
		err := t.db.SetTelegramEnabled(user.TelegramId, true, int(t.minLogLevel))
		if err != nil {
			t.reportError(chatId, "/start", err)
			return nil
		}
		t.plainResponse(chatId, "Notifications ENABLED")
		t.loadUsers()
		return nil
	}

	// Case 2: Known pending user
	if user != nil && user.IsPending() {
		t.plainResponse(chatId, "Your registration is awaiting admin approval\\.")
		return nil
	}

	// Case 3: Unknown user — register
	username := ctx.EffectiveUser.Username

	// Check for invite code in args (/start CODE via deep link)
	args := strings.Fields(ctx.EffectiveMessage.Text)
	hasValidCode := false
	if len(args) > 1 {
		code := args[1]
		err := t.db.UseInviteCode(code, chatId)
		if err == nil {
			hasValidCode = true
		}
	}

	err := t.db.RegisterTelegramUser(chatId, username)
	if err != nil {
		t.reportError(chatId, "/start register", err)
		return nil
	}

	if hasValidCode || !t.config.RequireApproval {
		// Auto-approve with valid invite code or when approval not required
		err = t.db.SetTelegramRole(chatId, entity.RoleUser)
		if err != nil {
			t.reportError(chatId, "/start approve", err)
			return nil
		}

		// Set default topic to invoice only for new users
		_ = t.db.SetTelegramTopics(chatId, []string{entity.TopicInvoice})

		t.plainResponse(chatId, "Welcome\\! You have been approved\\. Notifications are now ENABLED\\.")
		t.setUserCommands(chatId, entity.RoleUser)
		t.notifyAdmins(fmt.Sprintf("New user auto\\-approved: @%s \\(%d\\)", Sanitize(username), chatId))
	} else {
		t.plainResponse(chatId, "Registration received\\. An admin will review your request\\.")
		t.setUserCommands(chatId, entity.RolePending)
		// Notify admins with approve/revoke buttons
		keyboard := buildPendingUserButtons(chatId)
		t.mu.RLock()
		adminIds := make([]int64, len(t.adminIds))
		copy(adminIds, t.adminIds)
		t.mu.RUnlock()
		for _, adminId := range adminIds {
			t.sendWithKeyboard(adminId,
				fmt.Sprintf("New pending registration: @%s \\(%d\\)", Sanitize(username), chatId),
				keyboard,
			)
		}
	}

	t.loadUsers()
	return nil
}

// stop disables notifications for the calling user. Requires approved role.
func (t *TgBot) stop(_ *tgbotapi.Bot, ctx *ext.Context) error {
	if t.db == nil {
		return nil
	}
	chatId := ctx.EffectiveUser.Id
	if !t.requireApproved(chatId) {
		return nil
	}

	user := t.findUser(chatId)
	if user == nil {
		return nil
	}

	err := t.db.SetTelegramEnabled(user.TelegramId, false, user.LogLevel)
	if err != nil {
		t.reportError(chatId, "/stop", err)
		return nil
	}
	t.plainResponse(chatId, "Notifications DISABLED")
	t.loadUsers()
	return nil
}

// level shows an inline keyboard to select the minimum log level for notifications.
func (t *TgBot) level(_ *tgbotapi.Bot, ctx *ext.Context) error {
	if t.db == nil {
		return nil
	}
	chatId := ctx.EffectiveUser.Id
	if !t.requireApproved(chatId) {
		t.plainResponse(chatId, "You need to be approved first\\.")
		return nil
	}

	user := t.findUser(chatId)
	if user == nil {
		return nil
	}

	keyboard := buildLevelKeyboard(user.LogLevel)
	t.sendWithKeyboard(chatId, "*Log level*\nSelect minimum level:", keyboard)
	return nil
}

// topics shows an inline keyboard with toggle buttons for each notification topic.
func (t *TgBot) topics(_ *tgbotapi.Bot, ctx *ext.Context) error {
	if t.db == nil {
		return nil
	}
	chatId := ctx.EffectiveUser.Id
	if !t.requireApproved(chatId) {
		t.plainResponse(chatId, "You need to be approved first\\.")
		return nil
	}

	user := t.findUser(chatId)
	if user == nil {
		return nil
	}

	keyboard := buildTopicsKeyboard(user)
	t.sendWithKeyboard(chatId, "*Topic subscriptions*\nTap a topic to toggle:", keyboard)
	return nil
}

// subscribe adds a topic to the user's subscription list. Text-based fallback for /topics.
func (t *TgBot) subscribe(_ *tgbotapi.Bot, ctx *ext.Context) error {
	if t.db == nil {
		return nil
	}
	chatId := ctx.EffectiveUser.Id
	if !t.requireApproved(chatId) {
		t.plainResponse(chatId, "You need to be approved first\\.")
		return nil
	}

	user := t.findUser(chatId)
	if user == nil {
		return nil
	}

	args := strings.Fields(ctx.EffectiveMessage.Text)
	if len(args) < 2 {
		t.plainResponse(chatId, "Usage: `/subscribe <topic|all>`\nAvailable topics: "+Sanitize(strings.Join(entity.UserTopics(), ", ")))
		return nil
	}

	topic := strings.ToLower(args[1])

	if topic == "all" {
		err := t.db.SetTelegramTopics(chatId, nil)
		if err != nil {
			t.reportError(chatId, "/subscribe all", err)
			return nil
		}
		t.plainResponse(chatId, "Subscribed to *all* topics\\.")
		t.loadUsers()
		return nil
	}

	if !entity.IsUserTopic(topic) {
		t.plainResponse(chatId, "Invalid topic: `"+Sanitize(topic)+"`\nAvailable: "+Sanitize(strings.Join(entity.UserTopics(), ", ")))
		return nil
	}

	// Add topic if not already present
	currentTopics := user.TelegramTopics
	// Remove "none" sentinel if present
	filtered := make([]string, 0, len(currentTopics))
	for _, ct := range currentTopics {
		if ct != "none" && ct != topic {
			filtered = append(filtered, ct)
		}
	}
	filtered = append(filtered, topic)

	err := t.db.SetTelegramTopics(chatId, filtered)
	if err != nil {
		t.reportError(chatId, "/subscribe", err)
		return nil
	}
	t.plainResponse(chatId, "Subscribed to `"+Sanitize(topic)+"`")
	t.loadUsers()
	return nil
}

// unsubscribe removes a topic from the user's subscription list. Text-based fallback for /topics.
func (t *TgBot) unsubscribe(_ *tgbotapi.Bot, ctx *ext.Context) error {
	if t.db == nil {
		return nil
	}
	chatId := ctx.EffectiveUser.Id
	if !t.requireApproved(chatId) {
		t.plainResponse(chatId, "You need to be approved first\\.")
		return nil
	}

	user := t.findUser(chatId)
	if user == nil {
		return nil
	}

	args := strings.Fields(ctx.EffectiveMessage.Text)
	if len(args) < 2 {
		t.plainResponse(chatId, "Usage: `/unsubscribe <topic|all>`\nAvailable topics: "+Sanitize(strings.Join(entity.UserTopics(), ", ")))
		return nil
	}

	topic := strings.ToLower(args[1])

	if topic == "all" {
		err := t.db.SetTelegramTopics(chatId, []string{"none"})
		if err != nil {
			t.reportError(chatId, "/unsubscribe all", err)
			return nil
		}
		t.plainResponse(chatId, "Unsubscribed from all topics\\.")
		t.loadUsers()
		return nil
	}

	if !entity.IsUserTopic(topic) {
		t.plainResponse(chatId, "Invalid topic: `"+Sanitize(topic)+"`\nAvailable: "+Sanitize(strings.Join(entity.UserTopics(), ", ")))
		return nil
	}

	// If user currently has empty topics (subscribed to all), populate with all except the one being removed
	currentTopics := user.TelegramTopics
	if len(currentTopics) == 0 {
		currentTopics = entity.UserTopics()
	}

	filtered := make([]string, 0, len(currentTopics))
	for _, ct := range currentTopics {
		if ct != topic {
			filtered = append(filtered, ct)
		}
	}
	if len(filtered) == 0 {
		filtered = []string{"none"}
	}

	err := t.db.SetTelegramTopics(chatId, filtered)
	if err != nil {
		t.reportError(chatId, "/unsubscribe", err)
		return nil
	}
	t.plainResponse(chatId, "Unsubscribed from `"+Sanitize(topic)+"`")
	t.loadUsers()
	return nil
}

// tier shows an inline keyboard to select the notification delivery mode (realtime/critical/digest).
func (t *TgBot) tier(_ *tgbotapi.Bot, ctx *ext.Context) error {
	if t.db == nil {
		return nil
	}
	chatId := ctx.EffectiveUser.Id
	if !t.requireApproved(chatId) {
		t.plainResponse(chatId, "You need to be approved first\\.")
		return nil
	}

	user := t.findUser(chatId)
	if user == nil {
		return nil
	}

	keyboard := buildTierKeyboard(user.SubscriptionTier)
	t.sendWithKeyboard(chatId, "*Notification tier*\nSelect delivery mode:", keyboard)
	return nil
}

// status displays the user's current settings: role, enabled, level, tier, topics.
func (t *TgBot) status(_ *tgbotapi.Bot, ctx *ext.Context) error {
	if t.db == nil {
		return nil
	}
	chatId := ctx.EffectiveUser.Id
	if !t.requireApproved(chatId) {
		t.plainResponse(chatId, "You need to be approved first\\.")
		return nil
	}

	user := t.findUser(chatId)
	if user == nil {
		return nil
	}

	tier := string(user.SubscriptionTier)
	if tier == "" {
		tier = string(entity.TierRealtime)
	}

	topics := "all"
	if len(user.TelegramTopics) > 0 {
		topics = strings.Join(user.TelegramTopics, ", ")
	}

	enabled := "yes"
	if !user.TelegramEnabled {
		enabled = "no"
	}

	msg := fmt.Sprintf(
		"*Your Settings*\n"+
			"Role: `%s`\n"+
			"Enabled: `%s`\n"+
			"Log level: `%s`\n"+
			"Tier: `%s`\n"+
			"Topics: `%s`",
		Sanitize(string(user.TelegramRole)),
		enabled,
		Sanitize(slog.Level(user.LogLevel).String()),
		Sanitize(tier),
		Sanitize(topics),
	)
	t.plainResponse(chatId, msg)
	return nil
}

// help lists available commands, filtered by the caller's role.
func (t *TgBot) help(_ *tgbotapi.Bot, ctx *ext.Context) error {
	chatId := ctx.EffectiveUser.Id
	isAdmin := t.requireAdmin(chatId)
	isApproved := t.requireApproved(chatId)

	var sb strings.Builder
	sb.WriteString("*Available Commands*\n\n")

	sb.WriteString("`/start` \\- Register or enable notifications\n")
	sb.WriteString("`/help` \\- Show this help\n")

	if isApproved {
		sb.WriteString("\n*User Commands:*\n")
		sb.WriteString("`/stop` \\- Disable notifications\n")
		sb.WriteString("`/level` \\- Set log level\n")
		sb.WriteString("`/topics` \\- Manage topic subscriptions\n")
		sb.WriteString("`/tier` \\- Set notification tier\n")
		sb.WriteString("`/status` \\- Show your settings\n")
	}

	if isAdmin {
		sb.WriteString("\n*Admin Commands:*\n")
		sb.WriteString("`/users` \\- List all users\n")
		sb.WriteString("`/approve <id|@user>` \\- Approve a user\n")
		sb.WriteString("`/revoke <id|@user>` \\- Revoke a user\n")
		sb.WriteString("`/admin <id|@user>` \\- Promote to admin\n")
		sb.WriteString("`/invite` \\- Generate invite code\n")
	}

	t.plainResponse(chatId, sb.String())
	return nil
}
