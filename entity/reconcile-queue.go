package entity

import "time"

// HeldPaymentSummary is a lightweight view of an unresolved held payment awaiting
// reconciliation, returned by the reconciler queue endpoint. It omits the heavy line
// item and client detail payloads of CheckoutParams, exposing only what is needed to
// identify a queued hold.
type HeldPaymentSummary struct {
	OrderId   string    `json:"order_id"`
	PaymentId string    `json:"payment_id"`
	SessionId string    `json:"session_id,omitempty"`
	Total     int64     `json:"total"`
	Currency  string    `json:"currency,omitempty"`
	Created   time.Time `json:"created"`
}
