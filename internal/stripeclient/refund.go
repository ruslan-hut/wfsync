package stripeclient

import (
	"fmt"
	"log/slog"
	"strings"
	"time"
	"wfsync/entity"
	"wfsync/lib/sl"

	"github.com/stripe/stripe-go/v76"
)

// RefundState is the live refund picture of one PaymentIntent: what was captured, how
// much of it Stripe has refunded (charge.amount_refunded) and every refund on it.
type RefundState struct {
	PaymentId      string
	Currency       string
	AmountReceived int64
	AmountRefunded int64
	Refunds        []*entity.Refund
}

// Refund refunds a captured payment, fully (amount 0: whatever is still unrefunded) or
// partially. Only a succeeded PaymentIntent can be refunded: an uncaptured hold is
// released with CancelPayment instead. The over-refund check is left to Stripe, which
// rejects an amount above the unrefunded remainder. idempotencyKey is passed to Stripe so
// a repeated call with the same key and amount returns the same refund (within Stripe's
// 24 h key lifetime; the caller keeps a longer-lived local check). Returns the checkout
// params resolved from the session even on failure, for logging.
func (s *StripeClient) Refund(sessionId string, amount int64, reason, requestedBy, idempotencyKey string) (*entity.Refund, *entity.CheckoutParams, error) {
	log := s.log.With(
		slog.String("session_id", sessionId),
		slog.Int64("amount", amount),
	)

	params, err := s.db.GetCheckoutParamsSession(sessionId)
	if err != nil {
		log.Warn("failed to get checkout params from database", sl.Err(err))
		return nil, nil, fmt.Errorf("session not found")
	}
	if params == nil {
		return nil, nil, fmt.Errorf("session not found")
	}
	if params.PaymentId == "" {
		return nil, params, fmt.Errorf("payment id not found")
	}
	log = log.With(
		slog.String("order_id", params.OrderId),
		slog.String("payment_id", params.PaymentId),
	)

	pi, err := s.sc.PaymentIntents.Get(params.PaymentId, nil)
	if err != nil {
		return nil, params, fmt.Errorf("stripe response: %w", s.parseErr(err))
	}
	if pi.Status != stripe.PaymentIntentStatusSucceeded {
		return nil, params, fmt.Errorf("payment is %s: only a captured payment can be refunded, release a hold with cancel", pi.Status)
	}

	rp := &stripe.RefundParams{
		PaymentIntent: stripe.String(params.PaymentId),
		Metadata: map[string]string{
			"order_id":        params.OrderId,
			"reason":          reason,
			"requested_by":    requestedBy,
			"idempotency_key": idempotencyKey,
		},
	}
	if amount > 0 {
		rp.Amount = stripe.Int64(amount)
	}
	if isStripeRefundReason(reason) {
		rp.Reason = stripe.String(reason)
	}
	// Prefixed so a caller's key cannot collide with keys of other integrations on the account.
	rp.SetIdempotencyKey("wfsync-refund:" + idempotencyKey)

	r, err := s.sc.Refunds.New(rp)
	if err != nil {
		return nil, params, fmt.Errorf("stripe response: %w", s.parseErr(err))
	}

	refund := refundFromStripe(r)
	refund.SessionId = params.SessionId
	refund.OrderId = params.OrderId
	log.Info("refund created",
		slog.String("refund_id", refund.RefundId),
		slog.Int64("refunded", refund.Amount),
		slog.String("status", refund.Status),
	)
	return refund, params, nil
}

// PaymentRefunds reads the live refund state of a PaymentIntent.
func (s *StripeClient) PaymentRefunds(paymentId string) (*RefundState, error) {
	pi, err := s.sc.PaymentIntents.Get(paymentId, &stripe.PaymentIntentParams{
		Expand: []*string{stripe.String("latest_charge")},
	})
	if err != nil {
		return nil, fmt.Errorf("get payment intent: %w", s.parseErr(err))
	}
	st := &RefundState{
		PaymentId:      pi.ID,
		Currency:       strings.ToUpper(string(pi.Currency)),
		AmountReceived: pi.AmountReceived,
	}
	if pi.LatestCharge != nil {
		st.AmountRefunded = pi.LatestCharge.AmountRefunded
	}

	iter := s.sc.Refunds.List(&stripe.RefundListParams{PaymentIntent: stripe.String(paymentId)})
	for iter.Next() {
		st.Refunds = append(st.Refunds, refundFromStripe(iter.Refund()))
	}
	if err = iter.Err(); err != nil {
		return nil, fmt.Errorf("list refunds: %w", s.parseErr(err))
	}
	return st, nil
}

// RecentRefunds lists the PaymentIntent ids that got a refund created at or after since,
// across the whole account, so refunds made in the Stripe Dashboard can be found without
// polling every paid order. Refunds of plain charges (no PaymentIntent) are skipped.
func (s *StripeClient) RecentRefunds(since time.Time) ([]string, error) {
	lp := &stripe.RefundListParams{
		CreatedRange: &stripe.RangeQueryParams{GreaterThanOrEqual: since.Unix()},
	}
	seen := make(map[string]bool)
	var ids []string
	iter := s.sc.Refunds.List(lp)
	for iter.Next() {
		r := iter.Refund()
		if r.PaymentIntent == nil || r.PaymentIntent.ID == "" || seen[r.PaymentIntent.ID] {
			continue
		}
		seen[r.PaymentIntent.ID] = true
		ids = append(ids, r.PaymentIntent.ID)
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("list refunds: %w", s.parseErr(err))
	}
	return ids, nil
}

func refundFromStripe(r *stripe.Refund) *entity.Refund {
	refund := &entity.Refund{
		RefundId:     r.ID,
		Amount:       r.Amount,
		Currency:     strings.ToUpper(string(r.Currency)),
		Status:       string(r.Status),
		StripeReason: string(r.Reason),
		Created:      time.Unix(r.Created, 0).UTC(),
	}
	if r.PaymentIntent != nil {
		refund.PaymentId = r.PaymentIntent.ID
	}
	if r.Metadata != nil {
		refund.Reason = r.Metadata["reason"]
		refund.RequestedBy = r.Metadata["requested_by"]
		refund.IdempotencyKey = r.Metadata["idempotency_key"]
	}
	return refund
}

// isStripeRefundReason reports whether reason is one of the values Stripe accepts in a
// refund's reason field; any other text is kept only in metadata.
func isStripeRefundReason(reason string) bool {
	switch stripe.RefundReason(reason) {
	case stripe.RefundReasonDuplicate, stripe.RefundReasonFraudulent, stripe.RefundReasonRequestedByCustomer:
		return true
	}
	return false
}
