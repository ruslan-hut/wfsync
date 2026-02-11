package bot

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"wfsync/entity"
	"wfsync/lib/sl"

	tgbotapi "github.com/PaulSonOfLars/gotgbot/v2"
)

func (t *TgBot) plainResponse(chatId int64, text string) {
	if text == "" {
		t.log.With("id", chatId).Debug("empty message")
		return
	}

	_, err := t.api.SendMessage(chatId, text, &tgbotapi.SendMessageOpts{
		ParseMode: "MarkdownV2",
	})
	if err != nil {
		t.log.With(slog.Int64("id", chatId)).Warn("sending message", sl.Err(err))
		_, _ = t.api.SendMessage(chatId, err.Error(), &tgbotapi.SendMessageOpts{})
		_, err = t.api.SendMessage(chatId, text, &tgbotapi.SendMessageOpts{})
		if err != nil {
			t.log.With(slog.Int64("id", chatId)).Error("sending safe message", sl.Err(err))
		}
	}
}

func Sanitize(input string) string {
	reservedChars := "\\_{}#+-.!|()[]=*"
	sanitized := ""
	for _, char := range input {
		if strings.ContainsRune(reservedChars, char) {
			sanitized += "\\" + string(char)
		} else {
			sanitized += string(char)
		}
	}
	return sanitized
}

func (t *TgBot) requireAdmin(chatId int64) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	user, ok := t.users[chatId]
	if !ok {
		return false
	}
	return user.IsAdmin()
}

func (t *TgBot) requireApproved(chatId int64) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	user, ok := t.users[chatId]
	if !ok {
		return false
	}
	return user.IsApproved()
}

func (t *TgBot) findUserByUsername(username string) *entity.User {
	username = strings.TrimPrefix(username, "@")
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, user := range t.users {
		if strings.EqualFold(user.TelegramUsername, username) {
			return user
		}
	}
	return nil
}

// resolveUser finds a user by @username or numeric telegram ID string.
func (t *TgBot) resolveUser(identifier string) *entity.User {
	if strings.HasPrefix(identifier, "@") {
		return t.findUserByUsername(identifier)
	}
	id, err := strconv.ParseInt(identifier, 10, 64)
	if err != nil {
		return nil
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	user, ok := t.users[id]
	if !ok {
		return nil
	}
	return user
}

func (t *TgBot) notifyAdmins(msg string) {
	t.mu.RLock()
	adminIds := make([]int64, len(t.adminIds))
	copy(adminIds, t.adminIds)
	t.mu.RUnlock()

	for _, id := range adminIds {
		t.plainResponse(id, msg)
	}
}

func splitMessage(text string, maxLen int) []string {
	if len(text) <= maxLen {
		return []string{text}
	}
	var parts []string
	for len(text) > 0 {
		if len(text) <= maxLen {
			parts = append(parts, text)
			break
		}
		// Try to split at newline
		cutAt := maxLen
		nlIdx := strings.LastIndex(text[:maxLen], "\n")
		if nlIdx > 0 {
			cutAt = nlIdx + 1
		}
		parts = append(parts, text[:cutAt])
		text = text[cutAt:]
	}
	return parts
}

func userDisplayName(user *entity.User) string {
	if user.TelegramUsername != "" {
		return fmt.Sprintf("@%s (%d)", user.TelegramUsername, user.TelegramId)
	}
	return fmt.Sprintf("%d", user.TelegramId)
}

// sendWithKeyboard sends a message with an inline keyboard attached.
func (t *TgBot) sendWithKeyboard(chatId int64, text string, keyboard tgbotapi.InlineKeyboardMarkup) {
	if text == "" {
		return
	}
	_, err := t.api.SendMessage(chatId, text, &tgbotapi.SendMessageOpts{
		ParseMode:   "MarkdownV2",
		ReplyMarkup: keyboard,
	})
	if err != nil {
		t.log.With(slog.Int64("id", chatId)).Warn("sending message with keyboard", sl.Err(err))
		// Fallback: try without markdown
		_, err = t.api.SendMessage(chatId, text, &tgbotapi.SendMessageOpts{
			ReplyMarkup: keyboard,
		})
		if err != nil {
			t.log.With(slog.Int64("id", chatId)).Error("sending message with keyboard fallback", sl.Err(err))
		}
	}
}

// sanitizeUserTopics removes topics that are no longer allowed for each user's role.
// Called once on startup to clean up stale data after topic list changes.
func (t *TgBot) sanitizeUserTopics() {
	if t.db == nil {
		return
	}

	t.mu.RLock()
	users := make([]*entity.User, 0, len(t.users))
	for _, u := range t.users {
		users = append(users, u)
	}
	t.mu.RUnlock()

	for _, user := range users {
		t.sanitizeUserTopicsSingle(user)
	}

	// Reload after cleanup
	t.loadUsers()
}

// sanitizeUserTopicsSingle checks a single user's topics against their allowed list
// and removes any that are no longer valid for their role.
func (t *TgBot) sanitizeUserTopicsSingle(user *entity.User) {
	if t.db == nil || len(user.TelegramTopics) == 0 {
		return
	}

	allowed := entity.TopicsForRole(user.TelegramRole)
	allowedSet := make(map[string]bool, len(allowed))
	for _, a := range allowed {
		allowedSet[a] = true
	}

	filtered := make([]string, 0, len(user.TelegramTopics))
	changed := false
	for _, topic := range user.TelegramTopics {
		if topic == "none" || allowedSet[topic] {
			filtered = append(filtered, topic)
		} else {
			changed = true
		}
	}

	if !changed {
		return
	}
	if len(filtered) == 0 {
		filtered = []string{"none"}
	}
	err := t.db.SetTelegramTopics(user.TelegramId, filtered)
	if err != nil {
		t.log.Warn("sanitizing topics",
			slog.Int64("user_id", user.TelegramId),
			sl.Err(err),
		)
	} else {
		t.log.Info("sanitized topics",
			slog.Int64("user_id", user.TelegramId),
			slog.Any("removed_from", user.TelegramTopics),
			slog.Any("kept", filtered),
		)
	}
}

// reportError logs the error, notifies admins with details, and sends a neutral message to the user.
func (t *TgBot) reportError(chatId int64, command string, err error) {
	t.log.Error("bot command failed",
		slog.String("command", command),
		slog.Int64("user_id", chatId),
		sl.Err(err),
	)
	t.notifyAdmins(fmt.Sprintf(
		"Command `%s` failed\nUser: `%d`\nError: `%s`",
		Sanitize(command), chatId, Sanitize(err.Error()),
	))
	t.plainResponse(chatId, "Something went wrong\\. Please try again later\\.")
}
