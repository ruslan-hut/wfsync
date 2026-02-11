// Package entity defines domain types shared across the application.

package entity

// Notification topics used to categorize bot messages.
// Users subscribe to topics to filter which notifications they receive.
// Log calls can tag messages with slog.String("tg_topic", entity.TopicXxx).
const (
	TopicPayment  = "payment"
	TopicInvoice  = "invoice"
	TopicError    = "error"
	TopicSystem   = "system"
	TopicOrder    = "order"
	TopicSecurity = "security"
)

var allTopics = []string{
	TopicPayment,
	TopicInvoice,
	TopicError,
	TopicSystem,
	TopicOrder,
	TopicSecurity,
}

func AllTopics() []string {
	result := make([]string, len(allTopics))
	copy(result, allTopics)
	return result
}

func IsValidTopic(topic string) bool {
	for _, t := range allTopics {
		if t == topic {
			return true
		}
	}
	return false
}
