package entity

import "time"

// RetryJobStatus represents the current state of a retry job.
type RetryJobStatus string

const (
	RetryJobPending   RetryJobStatus = "pending"
	RetryJobCompleted RetryJobStatus = "completed"
	RetryJobFailed    RetryJobStatus = "failed"
)

// RetryJob tracks a failed invoice creation that should be retried with exponential backoff.
// The ID is set to EventId for idempotent upserts — each Stripe event produces at most one retry job.
type RetryJob struct {
	ID          string         `json:"id" bson:"_id"`
	EventId     string         `json:"event_id" bson:"event_id"`
	OrderId     string         `json:"order_id" bson:"order_id"`
	Status      RetryJobStatus `json:"status" bson:"status"`
	Attempts    int            `json:"attempts" bson:"attempts"`
	MaxAttempts int            `json:"max_attempts" bson:"max_attempts"`
	LastError   string         `json:"last_error" bson:"last_error"`
	NextRetryAt time.Time      `json:"next_retry_at" bson:"next_retry_at"`
	CreatedAt   time.Time      `json:"created_at" bson:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at" bson:"updated_at"`
}
