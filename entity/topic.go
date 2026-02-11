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

// allTopics is the full set of topics used internally for routing.
var allTopics = []string{
	TopicPayment,
	TopicInvoice,
	TopicError,
	TopicSystem,
	TopicOrder,
	TopicSecurity,
}

// userTopics are topics available for regular users to subscribe/unsubscribe.
// Admin-only topics (system, order, security) are not shown to users.
var userTopics = []string{
	TopicInvoice,
	TopicPayment,
	TopicError,
}

// AllTopics returns all topics (used for admin routing and internal logic).
func AllTopics() []string {
	result := make([]string, len(allTopics))
	copy(result, allTopics)
	return result
}

// UserTopics returns topics available for regular user subscription.
func UserTopics() []string {
	result := make([]string, len(userTopics))
	copy(result, userTopics)
	return result
}

// IsValidTopic checks if a topic exists in the full topic list.
func IsValidTopic(topic string) bool {
	for _, t := range allTopics {
		if t == topic {
			return true
		}
	}
	return false
}

// IsUserTopic checks if a topic is available for regular user subscription.
func IsUserTopic(topic string) bool {
	for _, t := range userTopics {
		if t == topic {
			return true
		}
	}
	return false
}
