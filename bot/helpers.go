package bot

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"unicode/utf8"
	"wfsync/entity"
	"wfsync/lib/sl"

	tgbotapi "github.com/PaulSonOfLars/gotgbot/v2"
)

// tgMaxMessageLen is Telegram's maximum message length.
const tgMaxMessageLen = 4096

func (t *TgBot) plainResponse(chatId int64, text string) {
	if text == "" {
		t.log.With("id", chatId).Debug("empty message")
		return
	}

	for _, part := range splitMessage(text, tgMaxMessageLen) {
		_, err := t.api.SendMessage(chatId, part, &tgbotapi.SendMessageOpts{
			ParseMode: "MarkdownV2",
		})
		if err != nil {
			t.log.With(slog.Int64("id", chatId)).Warn("sending message", sl.Err(err))
			// Fallback: try without markdown, still respecting the limit
			_, err = t.api.SendMessage(chatId, part, &tgbotapi.SendMessageOpts{})
			if err != nil {
				t.log.With(slog.Int64("id", chatId)).Error("sending safe message", sl.Err(err))
			}
		}
	}
}

// Sanitize escapes every character Telegram MarkdownV2 reserves outside entities.
func Sanitize(input string) string {
	const reservedChars = "\\_*[]()~`>#+-=|{}.!"
	var sb strings.Builder
	sb.Grow(len(input))
	for _, char := range input {
		if strings.ContainsRune(reservedChars, char) {
			sb.WriteByte('\\')
		}
		sb.WriteRune(char)
	}
	return sb.String()
}

// EscapeCodeBlock escapes the characters Telegram MarkdownV2 requires inside a
// pre/code entity (backslash and backtick), so arbitrary error text — which may
// itself contain backticks — cannot break out of the fenced block.
func EscapeCodeBlock(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "`", "\\`")
	return s
}

// CodeBlock wraps text in a fenced pre block, cut to at most maxLen bytes so a
// verbose error (an HTML error page) does not flood the chat.
func CodeBlock(text string, maxLen int) string {
	if len(text) > maxLen {
		cut := maxLen
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut] + "…"
	}
	return "```\n" + EscapeCodeBlock(text) + "\n```"
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

const codeFence = "```"

// splitMessage cuts MarkdownV2 text into parts of at most maxLen bytes. A part
// that ends inside a fenced block is closed and the next one reopens it, so each
// part parses on its own.
func splitMessage(text string, maxLen int) []string {
	if len(text) <= maxLen {
		return []string{text}
	}
	// Room for closing a fence at the end of a part and reopening it in the next.
	limit := maxLen - 2*(len(codeFence)+1)
	var parts []string
	reopen := false
	for len(text) > 0 {
		if reopen {
			text = codeFence + "\n" + text
		}
		if len(text) <= maxLen {
			parts = append(parts, text)
			break
		}
		// Try to split at newline
		cutAt := limit
		nlIdx := strings.LastIndex(text[:limit], "\n")
		if nlIdx > 0 {
			cutAt = nlIdx + 1
		} else {
			// No newline: stay on a rune boundary and keep an escape pair together.
			for cutAt > 0 && !utf8.RuneStart(text[cutAt]) {
				cutAt--
			}
			if trailingBackslashes(text[:cutAt])%2 == 1 {
				cutAt--
			}
			if cutAt <= 0 {
				cutAt = limit
			}
		}
		part := text[:cutAt]
		text = text[cutAt:]
		reopen = strings.Count(part, codeFence)%2 == 1
		if reopen {
			if !strings.HasSuffix(part, "\n") {
				part += "\n"
			}
			part += codeFence
		}
		parts = append(parts, part)
	}
	return parts
}

func trailingBackslashes(s string) int {
	n := 0
	for n < len(s) && s[len(s)-1-n] == '\\' {
		n++
	}
	return n
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

	changed := false
	for _, user := range users {
		if t.sanitizeUserTopicsSingle(user) {
			changed = true
		}
	}

	// Reload only when sanitization actually modified DB state.
	if changed {
		t.loadUsers()
	}
}

// sanitizeUserTopicsSingle checks a single user's topics against their allowed list
// and removes any that are no longer valid for their role.
// Returns true if the DB was updated.
func (t *TgBot) sanitizeUserTopicsSingle(user *entity.User) bool {
	if t.db == nil || len(user.TelegramTopics) == 0 {
		return false
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
		return false
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
		return false
	}
	t.log.Info("sanitized topics",
		slog.Int64("user_id", user.TelegramId),
		slog.Any("removed_from", user.TelegramTopics),
		slog.Any("kept", filtered),
	)
	return true
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
