package bot

import (
	"log/slog"
	"wfsync/entity"
)

// SendMessage broadcasts a message at the bot's minimum log level with no topic.
// Entry point for simple notifications that don't need topic filtering.
func (t *TgBot) SendMessage(msg string) {
	t.SendMessageWithLevel(msg, t.minLogLevel)
}

// SendMessageWithLevel sends a message to all enabled users filtered by log level.
// Delegates to SendMessageWithTopic with an inferred topic.
func (t *TgBot) SendMessageWithLevel(msg string, level slog.Level) {
	topic := entity.TopicSystem
	if level >= slog.LevelError {
		topic = entity.TopicError
	}
	t.SendMessageWithTopic(msg, level, topic)
}

// SendMessageWithTopic is the core notification routing method.
// For each cached user it checks: enabled → approved → log level ≥ user level → topic match.
// Then dispatches based on the user's subscription tier:
//   - realtime: immediate send
//   - critical: immediate send only if level ≥ ERROR
//   - digest:   buffer in DigestBuffer for periodic flush
func (t *TgBot) SendMessageWithTopic(msg string, level slog.Level, topic string) {
	t.mu.RLock()
	users := make(map[int64]*entity.User, len(t.users))
	for k, v := range t.users {
		users[k] = v
	}
	t.mu.RUnlock()

	l := int(level)
	for _, user := range users {
		if !user.TelegramEnabled || !user.IsApproved() {
			continue
		}
		if l < user.LogLevel {
			continue
		}
		if !user.HasTopic(topic) {
			continue
		}

		tier := user.SubscriptionTier
		if tier == "" {
			tier = entity.TierRealtime
		}

		switch tier {
		case entity.TierRealtime:
			t.plainResponse(user.TelegramId, msg)
		case entity.TierCritical:
			if level >= slog.LevelError {
				t.plainResponse(user.TelegramId, msg)
			}
		case entity.TierDigest:
			if t.digest != nil {
				t.digest.Add(user.TelegramId, msg, topic, level)
			}
		}
	}
}
