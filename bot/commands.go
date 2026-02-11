package bot

import (
	"fmt"
	"log/slog"
	"strings"
	"wfsync/entity"

	tgbotapi "github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
)

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
		t.notifyAdmins(fmt.Sprintf("New user auto\\-approved: @%s \\(%d\\)", Sanitize(username), chatId))
	} else {
		t.plainResponse(chatId, "Registration received\\. An admin will review your request\\.")
		t.notifyAdmins(fmt.Sprintf("New pending registration: @%s \\(%d\\)\\. Use `/approve %d` to approve\\.", Sanitize(username), chatId, chatId))
	}

	t.loadUsers()
	return nil
}

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

	args := strings.Fields(ctx.EffectiveMessage.Text)
	if len(args) < 2 {
		currentLevel := slog.Level(user.LogLevel).String()
		t.plainResponse(chatId, fmt.Sprintf("Your current log level: %s\nAvailable levels: debug, info, warn, error", Sanitize(currentLevel)))
		return nil
	}

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
		t.plainResponse(chatId, fmt.Sprintf("Invalid level: %s\nAvailable levels: debug, info, warn, error", Sanitize(levelStr)))
		return nil
	}

	err := t.db.SetTelegramEnabled(user.TelegramId, true, int(level))
	if err != nil {
		t.reportError(chatId, "/level", err)
		return nil
	}
	t.plainResponse(chatId, fmt.Sprintf("Log level set to: %s", Sanitize(level.String())))
	t.loadUsers()
	return nil
}

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

	allTopics := entity.AllTopics()
	var sb strings.Builder
	sb.WriteString("*Available topics:*\n")
	for _, topic := range allTopics {
		subscribed := user.HasTopic(topic)
		marker := "  "
		if subscribed {
			marker = "\\+ "
		}
		sb.WriteString(fmt.Sprintf("%s`%s`\n", marker, topic))
	}

	if len(user.TelegramTopics) == 0 {
		sb.WriteString("\nYou are subscribed to *all* topics\\.")
	}

	sb.WriteString("\nUse `/subscribe <topic>` or `/unsubscribe <topic>`")
	t.plainResponse(chatId, sb.String())
	return nil
}

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
		t.plainResponse(chatId, "Usage: `/subscribe <topic|all>`\nAvailable topics: "+Sanitize(strings.Join(entity.AllTopics(), ", ")))
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

	if !entity.IsValidTopic(topic) {
		t.plainResponse(chatId, "Invalid topic: `"+Sanitize(topic)+"`\nAvailable: "+Sanitize(strings.Join(entity.AllTopics(), ", ")))
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
		t.plainResponse(chatId, "Usage: `/unsubscribe <topic|all>`\nAvailable topics: "+Sanitize(strings.Join(entity.AllTopics(), ", ")))
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

	if !entity.IsValidTopic(topic) {
		t.plainResponse(chatId, "Invalid topic: `"+Sanitize(topic)+"`\nAvailable: "+Sanitize(strings.Join(entity.AllTopics(), ", ")))
		return nil
	}

	// If user currently has empty topics (subscribed to all), populate with all except the one being removed
	currentTopics := user.TelegramTopics
	if len(currentTopics) == 0 {
		currentTopics = entity.AllTopics()
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

	args := strings.Fields(ctx.EffectiveMessage.Text)
	if len(args) < 2 {
		currentTier := string(user.SubscriptionTier)
		if currentTier == "" {
			currentTier = string(entity.TierRealtime)
		}
		t.plainResponse(chatId, fmt.Sprintf("Your current tier: `%s`\nAvailable: realtime, critical, digest", Sanitize(currentTier)))
		return nil
	}

	tierStr := strings.ToLower(args[1])
	var newTier entity.SubscriptionTier
	switch tierStr {
	case "realtime":
		newTier = entity.TierRealtime
	case "critical":
		newTier = entity.TierCritical
	case "digest":
		newTier = entity.TierDigest
	default:
		t.plainResponse(chatId, "Invalid tier: `"+Sanitize(tierStr)+"`\nAvailable: realtime, critical, digest")
		return nil
	}

	err := t.db.SetSubscriptionTier(chatId, newTier, "")
	if err != nil {
		t.reportError(chatId, "/tier", err)
		return nil
	}
	t.plainResponse(chatId, "Subscription tier set to: `"+Sanitize(string(newTier))+"`")
	t.loadUsers()
	return nil
}

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
		sb.WriteString("`/level <debug|info|warn|error>` \\- Set log level\n")
		sb.WriteString("`/topics` \\- View topic subscriptions\n")
		sb.WriteString("`/subscribe <topic|all>` \\- Subscribe to topic\n")
		sb.WriteString("`/unsubscribe <topic|all>` \\- Unsubscribe from topic\n")
		sb.WriteString("`/tier <realtime|critical|digest>` \\- Set notification tier\n")
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
