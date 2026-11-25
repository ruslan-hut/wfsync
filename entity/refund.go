package entity

import (
	"fmt"
	"net/http"
	"time"
	"wfsync/lib/validate"
)

// Payment states written to oc_order.wf_payment_status besides Stripe's own statuses.
const (
	PaymentStatusPaid              = "paid"
	PaymentStatusCanceled          = "canceled"
	PaymentStatusRefunded          = "refunded"
	PaymentStatusPartiallyRefunded = "partially_refunded"
)

// Refund sources: RefundSourceAPI is a refund made through POST /v1/st/refund/{id};
// RefundSourceStripe is one first seen on the Stripe side (webhook or reconciler), e.g.
// made in the Stripe Dashboard.
const (
	RefundSourceAPI    = "api"
	RefundSourceStripe = "stripe"
)

// Refund is one Stripe refund of a captured PaymentIntent, stored in its own collection
// keyed by RefundId so the API, the webhook and the reconciler converge on one record.
type Refund struct {
	RefundId  string `json:"refund_id" bson:"refund_id"`
	PaymentId string `json:"payment_id" bson:"payment_id"`
	SessionId string `json:"session_id,omitempty" bson:"session_id,omitempty"`
	OrderId   string `json:"order_id,omitempty" bson:"order_id,omitempty"`
	// Amount is in minor units of Currency.
	Amount   int64  `json:"amount" bson:"amount"`
	Currency string `json:"currency,omitempty" bson:"currency,omitempty"`
	// Status is Stripe's refund status: pending, requires_action, succeeded, failed, canceled.
	Status string `json:"status" bson:"status"`
	// Reason is the caller's free-text reason; StripeReason is the enum Stripe kept, if any.
	Reason         string    `json:"reason,omitempty" bson:"reason,omitempty"`
	StripeReason   string    `json:"stripe_reason,omitempty" bson:"stripe_reason,omitempty"`
	RequestedBy    string    `json:"requested_by,omitempty" bson:"requested_by,omitempty"`
	IdempotencyKey string    `json:"idempotency_key,omitempty" bson:"idempotency_key,omitempty"`
	Source         string    `json:"source,omitempty" bson:"source,omitempty"`
	Created        time.Time `json:"created" bson:"created"`
	Recorded       time.Time `json:"recorded" bson:"recorded"`
}

// Counts reports whether the refund takes money back from the charge: failed and
// canceled refunds are reversed by Stripe and do not.
func (r *Refund) Counts() bool {
	return r.Status != "failed" && r.Status != "canceled"
}

// RefundRequest is the body of POST /v1/st/refund/{id}. Amount 0 refunds whatever is
// still unrefunded on the payment.
type RefundRequest struct {
	Amount int64  `json:"amount" validate:"min=0"`
	Reason string `json:"reason" validate:"max=255"`
}

func (r *RefundRequest) Bind(_ *http.Request) error {
	return validate.Struct(r)
}

// RefundResult is the response of a refund call: the refund itself and the payment's
// refund totals as Stripe reports them after it.
type RefundResult struct {
	Refund         *Refund `json:"refund"`
	OrderId        string  `json:"order_id,omitempty"`
	AmountReceived int64   `json:"amount_received"`
	AmountRefunded int64   `json:"amount_refunded"`
	PaymentStatus  string  `json:"payment_status"`
	// Replayed is true when the idempotency key matched an earlier refund and no new
	// refund was made.
	Replayed bool `json:"replayed,omitempty"`
}

// RefundState is the wf_payment_status of a captured payment given how much of it was
// refunded: paid, partially_refunded or refunded.
func RefundState(received, refunded int64) string {
	switch {
	case refunded <= 0:
		return PaymentStatusPaid
	case refunded >= received:
		return PaymentStatusRefunded
	default:
		return PaymentStatusPartiallyRefunded
	}
}

// ErrRefundKeyReused reports an idempotency key already used for another payment or
// another amount.
var ErrRefundKeyReused = fmt.Errorf("idempotency key already used for a different refund")
