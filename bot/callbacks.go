package bot

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"wfsync/entity"

	tgbotapi "github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
)

// Callback data prefixes for inline keyboard buttons.
// Telegram limits callback data to 64 bytes, so prefixes are kept short.
// Format: prefix + value (e.g., "t:payment", "a:123456").
const (
	cbTopicToggle = "t:"  // t:payment, t:all, t:none
	cbTier        = "tr:" // tr:realtime, tr:critical, tr:digest
	cbLevel       = "lv:" // lv:debug, lv:info, lv:warn, lv:error
	cbApprove     = "a:"  // a:<telegram_id>
	cbRevoke      = "r:"  // r:<telegram_id>
)

// --- Keyboard builders ---

// buildTopicsKeyboard creates an inline keyboard with toggle buttons for each topic.
// Admins see all topics; regular users see only user topics.
func buildTopicsKeyboard(user *entity.User) tgbotapi.InlineKeyboardMarkup {
	allTopics := entity.TopicsForRole(user.TelegramRole)
	rows := make([][]tgbotapi.InlineKeyboardButton, 0, len(allTopics)/2+2)

	// Topic buttons in rows of 2
	var row []tgbotapi.InlineKeyboardButton
	for i, topic := range allTopics {
		label := topic
		if user.HasTopic(topic) {
			label = topic + " ✓"
		}
		row = append(row, tgbotapi.InlineKeyboardButton{
			Text:         label,
			CallbackData: cbTopicToggle + topic,
		})
		if len(row) == 2 || i == len(allTopics)-1 {
			rows = append(rows, row)
			row = nil
		}
	}

	// Subscribe all / Unsubscribe all
	rows = append(rows, []tgbotapi.InlineKeyboardButton{
		{Text: "Subscribe all", CallbackData: cbTopicToggle + "all"},
		{Text: "Unsubscribe all", CallbackData: cbTopicToggle + "none"},
	})

	return tgbotapi.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// buildTierKeyboard creates an inline keyboard for tier selection.
func buildTierKeyboard(current entity.SubscriptionTier) tgbotapi.InlineKeyboardMarkup {
	if current == "" {
		current = entity.TierRealtime
	}
	tiers := []struct {
		tier  entity.SubscriptionTier
		label string
	}{
		{entity.TierRealtime, "Realtime"},
		{entity.TierCritical, "Critical only"},
		{entity.TierDigest, "Digest"},
	}

	var buttons []tgbotapi.InlineKeyboardButton
	for _, t := range tiers {
		label := t.label
		if t.tier == current {
			label += " ✓"
		}
		buttons = append(buttons, tgbotapi.InlineKeyboardButton{
			Text:         label,
			CallbackData: cbTier + string(t.tier),
		})
	}

	return tgbotapi.InlineKeyboardMarkup{
		InlineKeyboard: [][]tgbotapi.InlineKeyboardButton{buttons},
	}
}

// buildLevelKeyboard creates an inline keyboard for log level selection.
func buildLevelKeyboard(currentLevel int) tgbotapi.InlineKeyboardMarkup {
	levels := []struct {
		level slog.Level
		label string
	}{
		{slog.LevelDebug, "Debug"},
		{slog.LevelInfo, "Info"},
		{slog.LevelWarn, "Warn"},
		{slog.LevelError, "Error"},
	}

	var buttons []tgbotapi.InlineKeyboardButton
	for _, l := range levels {
		label := l.label
		if int(l.level) == currentLevel {
			label += " ✓"
		}
		buttons = append(buttons, tgbotapi.InlineKeyboardButton{
			Text:         label,
			CallbackData: cbLevel + strings.ToLower(l.label),
		})
	}

	return tgbotapi.InlineKeyboardMarkup{
		InlineKeyboard: [][]tgbotapi.InlineKeyboardButton{buttons},
	}
}

// buildPendingUserButtons creates approve/revoke buttons for a pending user.
func buildPendingUserButtons(telegramId int64) tgbotapi.InlineKeyboardMarkup {
	idStr := strconv.FormatInt(telegramId, 10)
	return tgbotapi.InlineKeyboardMarkup{
		InlineKeyboard: [][]tgbotapi.InlineKeyboardButton{
			{
				{Text: "Approve ✓", CallbackData: cbApprove + idStr},
				{Text: "Revoke ✗", CallbackData: cbRevoke + idStr},
			},
		},
	}
}

// --- Callback handlers ---
// All callback handlers follow the same pattern:
//  1. Verify authorization (approved/admin)
//  2. Parse callback data (trim prefix)
//  3. Update DB state
//  4. Reload users cache
//  5. Edit the keyboard in-place via EditMessageReplyMarkup
//  6. Answer the callback query (removes loading spinner)

// onTopicCallback handles topic toggle button presses.
// Supports individual topic toggle, "all" (subscribe to everything), and "none" (unsubscribe from all).
func (t *TgBot) onTopicCallback(_ *tgbotapi.Bot, ctx *ext.Context) error {
	cq := ctx.CallbackQuery
	chatId := cq.From.Id

	if !t.requireApproved(chatId) {
		_, _ = cq.Answer(t.api, &tgbotapi.AnswerCallbackQueryOpts{Text: "Not authorized", ShowAlert: true})
		return nil
	}

	user := t.findUser(chatId)
	if user == nil {
		_, _ = cq.Answer(t.api, &tgbotapi.AnswerCallbackQueryOpts{Text: "User not found", ShowAlert: true})
		return nil
	}

	topic := strings.TrimPrefix(cq.Data, cbTopicToggle)
	var answerText string

	switch topic {
	case "all":
		err := t.db.SetTelegramTopics(chatId, nil)
		if err != nil {
			t.reportError(chatId, "topic:all", err)
			_, _ = cq.Answer(t.api, &tgbotapi.AnswerCallbackQueryOpts{Text: "Error occurred"})
			return nil
		}
		answerText = "Subscribed to all topics"

	case "none":
		err := t.db.SetTelegramTopics(chatId, []string{"none"})
		if err != nil {
			t.reportError(chatId, "topic:none", err)
			_, _ = cq.Answer(t.api, &tgbotapi.AnswerCallbackQueryOpts{Text: "Error occurred"})
			return nil
		}
		answerText = "Unsubscribed from all topics"

	default:
		if !entity.IsTopicAllowedForRole(topic, user.TelegramRole) {
			_, _ = cq.Answer(t.api, &tgbotapi.AnswerCallbackQueryOpts{Text: "Invalid topic"})
			return nil
		}

		// Toggle: if subscribed, remove; if not, add
		if user.HasTopic(topic) {
			// Unsubscribe
			currentTopics := user.TelegramTopics
			if len(currentTopics) == 0 {
				currentTopics = entity.TopicsForRole(user.TelegramRole)
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
				t.reportError(chatId, "topic:unsub", err)
				_, _ = cq.Answer(t.api, &tgbotapi.AnswerCallbackQueryOpts{Text: "Error occurred"})
				return nil
			}
			answerText = "Unsubscribed from " + topic
		} else {
			// Subscribe
			currentTopics := user.TelegramTopics
			filtered := make([]string, 0, len(currentTopics)+1)
			for _, ct := range currentTopics {
				if ct != "none" && ct != topic {
					filtered = append(filtered, ct)
				}
			}
			filtered = append(filtered, topic)
			err := t.db.SetTelegramTopics(chatId, filtered)
			if err != nil {
				t.reportError(chatId, "topic:sub", err)
				_, _ = cq.Answer(t.api, &tgbotapi.AnswerCallbackQueryOpts{Text: "Error occurred"})
				return nil
			}
			answerText = "Subscribed to " + topic
		}
	}

	t.loadUsers()

	// Refresh the user to rebuild keyboard with updated state
	updatedUser := t.findUser(chatId)
	if updatedUser != nil {
		keyboard := buildTopicsKeyboard(updatedUser)
		if msg := cq.Message; msg != nil {
			if im, ok := msg.(tgbotapi.Message); ok {
				_, _, _ = t.api.EditMessageReplyMarkup(&tgbotapi.EditMessageReplyMarkupOpts{
					ChatId:      chatId,
					MessageId:   im.MessageId,
					ReplyMarkup: keyboard,
				})
			}
		}
	}

	_, _ = cq.Answer(t.api, &tgbotapi.AnswerCallbackQueryOpts{Text: answerText})
	return nil
}

// onTierCallback handles tier selection button presses (realtime/critical/digest).
func (t *TgBot) onTierCallback(_ *tgbotapi.Bot, ctx *ext.Context) error {
	cq := ctx.CallbackQuery
	chatId := cq.From.Id

	if !t.requireApproved(chatId) {
		_, _ = cq.Answer(t.api, &tgbotapi.AnswerCallbackQueryOpts{Text: "Not authorized", ShowAlert: true})
		return nil
	}

	tierStr := strings.TrimPrefix(cq.Data, cbTier)
	var newTier entity.SubscriptionTier
	switch tierStr {
	case "realtime":
		newTier = entity.TierRealtime
	case "critical":
		newTier = entity.TierCritical
	case "digest":
		newTier = entity.TierDigest
	default:
		_, _ = cq.Answer(t.api, &tgbotapi.AnswerCallbackQueryOpts{Text: "Invalid tier"})
		return nil
	}

	err := t.db.SetSubscriptionTier(chatId, newTier, "")
	if err != nil {
		t.reportError(chatId, "tier:set", err)
		_, _ = cq.Answer(t.api, &tgbotapi.AnswerCallbackQueryOpts{Text: "Error occurred"})
		return nil
	}

	t.loadUsers()

	// Update keyboard to reflect new selection
	keyboard := buildTierKeyboard(newTier)
	if msg := cq.Message; msg != nil {
		if im, ok := msg.(tgbotapi.Message); ok {
			_, _, _ = t.api.EditMessageReplyMarkup(&tgbotapi.EditMessageReplyMarkupOpts{
				ChatId:      chatId,
				MessageId:   im.MessageId,
				ReplyMarkup: keyboard,
			})
		}
	}

	_, _ = cq.Answer(t.api, &tgbotapi.AnswerCallbackQueryOpts{
		Text: "Tier set to " + tierStr,
	})
	return nil
}

// onLevelCallback handles log level selection button presses (debug/info/warn/error).
func (t *TgBot) onLevelCallback(_ *tgbotapi.Bot, ctx *ext.Context) error {
	cq := ctx.CallbackQuery
	chatId := cq.From.Id

	if !t.requireApproved(chatId) {
		_, _ = cq.Answer(t.api, &tgbotapi.AnswerCallbackQueryOpts{Text: "Not authorized", ShowAlert: true})
		return nil
	}

	levelStr := strings.TrimPrefix(cq.Data, cbLevel)
	var level slog.Level
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
		_, _ = cq.Answer(t.api, &tgbotapi.AnswerCallbackQueryOpts{Text: "Invalid level"})
		return nil
	}

	err := t.db.SetTelegramEnabled(chatId, true, int(level))
	if err != nil {
		t.reportError(chatId, "level:set", err)
		_, _ = cq.Answer(t.api, &tgbotapi.AnswerCallbackQueryOpts{Text: "Error occurred"})
		return nil
	}

	t.loadUsers()

	// Update keyboard
	keyboard := buildLevelKeyboard(int(level))
	if msg := cq.Message; msg != nil {
		if im, ok := msg.(tgbotapi.Message); ok {
			_, _, _ = t.api.EditMessageReplyMarkup(&tgbotapi.EditMessageReplyMarkupOpts{
				ChatId:      chatId,
				MessageId:   im.MessageId,
				ReplyMarkup: keyboard,
			})
		}
	}

	_, _ = cq.Answer(t.api, &tgbotapi.AnswerCallbackQueryOpts{
		Text: "Level set to " + levelStr,
	})
	return nil
}

// onApproveCallback handles the inline "Approve" button for pending users.
// After approval, replaces the buttons with a confirmation message.
func (t *TgBot) onApproveCallback(_ *tgbotapi.Bot, ctx *ext.Context) error {
	cq := ctx.CallbackQuery
	chatId := cq.From.Id

	if !t.requireAdmin(chatId) {
		_, _ = cq.Answer(t.api, &tgbotapi.AnswerCallbackQueryOpts{Text: "Admin access required", ShowAlert: true})
		return nil
	}

	idStr := strings.TrimPrefix(cq.Data, cbApprove)
	targetId, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		_, _ = cq.Answer(t.api, &tgbotapi.AnswerCallbackQueryOpts{Text: "Invalid user ID"})
		return nil
	}

	target := t.findUser(targetId)
	if target == nil {
		_, _ = cq.Answer(t.api, &tgbotapi.AnswerCallbackQueryOpts{Text: "User not found"})
		return nil
	}

	err = t.db.SetTelegramRole(target.TelegramId, entity.RoleUser)
	if err != nil {
		t.reportError(chatId, "approve:callback", err)
		_, _ = cq.Answer(t.api, &tgbotapi.AnswerCallbackQueryOpts{Text: "Error occurred"})
		return nil
	}

	_ = t.db.SetTelegramTopics(target.TelegramId, []string{entity.TopicInvoice})

	t.loadUsers()
	t.setUserCommands(target.TelegramId, entity.RoleUser)

	// Update the message to show result instead of buttons
	if msg := cq.Message; msg != nil {
		if im, ok := msg.(tgbotapi.Message); ok {
			_, _, _ = t.api.EditMessageText(
				fmt.Sprintf("%s\n\n✓ Approved by %s", im.Text, Sanitize(userDisplayName(t.findUser(chatId)))),
				&tgbotapi.EditMessageTextOpts{
					ChatId:    chatId,
					MessageId: im.MessageId,
				},
			)
		}
	}

	t.plainResponse(target.TelegramId, "Your registration has been approved\\! Notifications are now enabled\\.")

	_, _ = cq.Answer(t.api, &tgbotapi.AnswerCallbackQueryOpts{
		Text: "User approved",
	})
	return nil
}

// onRevokeCallback handles the inline "Revoke" button for pending users.
// After revocation, replaces the buttons with a confirmation message.
func (t *TgBot) onRevokeCallback(_ *tgbotapi.Bot, ctx *ext.Context) error {
	cq := ctx.CallbackQuery
	chatId := cq.From.Id

	if !t.requireAdmin(chatId) {
		_, _ = cq.Answer(t.api, &tgbotapi.AnswerCallbackQueryOpts{Text: "Admin access required", ShowAlert: true})
		return nil
	}

	idStr := strings.TrimPrefix(cq.Data, cbRevoke)
	targetId, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		_, _ = cq.Answer(t.api, &tgbotapi.AnswerCallbackQueryOpts{Text: "Invalid user ID"})
		return nil
	}

	target := t.findUser(targetId)
	if target == nil {
		_, _ = cq.Answer(t.api, &tgbotapi.AnswerCallbackQueryOpts{Text: "User not found"})
		return nil
	}

	err = t.db.SetTelegramRole(target.TelegramId, entity.RoleNone)
	if err != nil {
		t.reportError(chatId, "revoke:callback", err)
		_, _ = cq.Answer(t.api, &tgbotapi.AnswerCallbackQueryOpts{Text: "Error occurred"})
		return nil
	}

	t.loadUsers()
	t.setUserCommands(target.TelegramId, entity.RoleNone)

	// Update the message to show result instead of buttons
	if msg := cq.Message; msg != nil {
		if im, ok := msg.(tgbotapi.Message); ok {
			_, _, _ = t.api.EditMessageText(
				fmt.Sprintf("%s\n\n✗ Revoked by %s", im.Text, Sanitize(userDisplayName(t.findUser(chatId)))),
				&tgbotapi.EditMessageTextOpts{
					ChatId:    chatId,
					MessageId: im.MessageId,
				},
			)
		}
	}

	t.plainResponse(target.TelegramId, "Your access has been revoked\\.")

	_, _ = cq.Answer(t.api, &tgbotapi.AnswerCallbackQueryOpts{
		Text: "User revoked",
	})
	return nil
}
