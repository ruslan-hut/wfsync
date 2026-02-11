package entity

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
