package core

import (
	"fmt"
	"log/slog"
	"wfsync/entity"
	"wfsync/lib/sl"
)

// RefundDatabase stores Stripe refunds (collection stripe_refunds) and resolves the
// checkout params of a PaymentIntent for refunds first seen on the Stripe side.
type RefundDatabase interface {
	SaveRefund(r *entity.Refund) (bool, error)
	GetRefundsByPayment(paymentId string) ([]*entity.Refund, error)
	GetRefundByIdempotencyKey(key string) (*entity.Refund, error)
	GetCheckoutParamsByPayment(paymentId string) (*entity.CheckoutParams, error)
	GetCheckoutParamsSession(sessionId string) (*entity.CheckoutParams, error)
}

// lockPayment serializes refund work on one PaymentIntent — the refund API, the webhook
// and the reconciler — so a stale Stripe read cannot overwrite a fresher status and a
// webhook cannot store the API's refund between its creation and its recording.
func (c *Core) lockPayment(paymentId string) func() {
	if c.refundLocks == nil || paymentId == "" {
		return func() {}
	}
	return c.refundLocks.Lock(paymentId)
}

func (c *Core) SetRefundDatabase(db RefundDatabase) {
	c.refundDb = db
}

// StripeRefund refunds a captured payment by its checkout session id: amount 0 refunds
// the unrefunded remainder, any other amount is a partial refund (repeatable until the
// capture is used up — Stripe enforces the ceiling). idempotencyKey is required: a key
// already stored locally replays the earlier refund without calling Stripe, which keeps
// a retry safe past Stripe's own 24 h key window. The refund is recorded, the payment's
// refunds are synced from Stripe, and oc_order.wf_payment_status becomes
// partially_refunded / refunded. Returns the checkout params, when resolved, for logging.
func (c *Core) StripeRefund(sessionId string, req *entity.RefundRequest, requestedBy, idempotencyKey string) (*entity.RefundResult, *entity.CheckoutParams, error) {
	if c.sc == nil {
		return nil, nil, fmt.Errorf("stripe service not connected")
	}
	if c.refundDb == nil {
		return nil, nil, fmt.Errorf("refund database not connected")
	}
	if idempotencyKey == "" {
		return nil, nil, fmt.Errorf("idempotency key required")
	}

	// Lock before the key lookup so concurrent requests with the same key serialize and
	// the second one replays the first.
	if held, err := c.refundDb.GetCheckoutParamsSession(sessionId); err == nil && held != nil {
		defer c.lockPayment(held.PaymentId)()
	}

	prior, err := c.refundDb.GetRefundByIdempotencyKey(idempotencyKey)
	if err != nil {
		return nil, nil, fmt.Errorf("lookup idempotency key: %w", err)
	}
	if prior != nil {
		if prior.SessionId != sessionId || (req.Amount > 0 && req.Amount != prior.Amount) {
			return nil, nil, entity.ErrRefundKeyReused
		}
		params, err := c.refundDb.GetCheckoutParamsByPayment(prior.PaymentId)
		if err != nil || params == nil {
			return nil, nil, fmt.Errorf("checkout params for payment %s not found", prior.PaymentId)
		}
		res := c.refundResult(prior, params, nil)
		res.Replayed = true
		return res, params, nil
	}

	refund, params, err := c.sc.Refund(sessionId, req.Amount, req.Reason, requestedBy, idempotencyKey)
	if err != nil {
		return nil, params, err
	}
	refund.Reason = req.Reason
	refund.RequestedBy = requestedBy
	refund.IdempotencyKey = idempotencyKey
	refund.Source = entity.RefundSourceAPI
	// The money is already returned, so a failed save is logged, not reported: the
	// idempotency key also travels in the refund metadata and the sync restores it.
	if _, err = c.refundDb.SaveRefund(refund); err != nil {
		c.log.With(sl.Err(err), slog.String("refund_id", refund.RefundId)).Error("save refund")
	}

	state, err := c.syncLocked(params, refund.RefundId)
	if err != nil {
		c.log.With(sl.Err(err), slog.String("order_id", params.OrderId)).Error("sync refunds after refund")
	}
	c.notifyRefund(params, refund, state)

	return c.refundResult(refund, params, state), params, nil
}

// refundResult builds the API response from the payment's stored refunds, falling back
// to the refund alone when the store cannot be read. Live totals in snap, when present,
// take precedence over the ones summed from the store.
func (c *Core) refundResult(refund *entity.Refund, params *entity.CheckoutParams, snap *refundSnapshot) *entity.RefundResult {
	res := &entity.RefundResult{
		Refund:         refund,
		OrderId:        params.OrderId,
		AmountReceived: received(params),
	}
	refunds, err := c.refundDb.GetRefundsByPayment(refund.PaymentId)
	if err != nil || len(refunds) == 0 {
		refunds = []*entity.Refund{refund}
	}
	for _, r := range refunds {
		if r.RefundId == refund.RefundId {
			res.Refund = mergeRefund(refund, r)
		}
		if r.Counts() {
			res.AmountRefunded += r.Amount
		}
	}
	if snap != nil {
		res.AmountReceived = snap.received
		res.AmountRefunded = snap.refunded
	}
	res.PaymentStatus = entity.RefundState(res.AmountReceived, res.AmountRefunded)
	return res
}

// mergeRefund prefers the stored copy (it carries the latest status) but keeps fields
// only the in-memory one has.
func mergeRefund(mem, stored *entity.Refund) *entity.Refund {
	if stored.RequestedBy == "" {
		stored.RequestedBy = mem.RequestedBy
	}
	if stored.Reason == "" {
		stored.Reason = mem.Reason
	}
	return stored
}

// received is the captured amount of a payment: AmountPaid when a capture path recorded
// it, otherwise the order total.
func received(params *entity.CheckoutParams) int64 {
	if params.AmountPaid > 0 {
		return params.AmountPaid
	}
	return params.Total
}

// syncPaymentRefunds pulls a payment's refunds from Stripe, stores each one and writes
// the resulting wf_payment_status to OpenCart. Refunds stored for the first time — made
// outside the refund API, e.g. in the Stripe Dashboard — are signalled; skipRefundId is
// the refund the caller signals itself. Shared by the refund API, the charge.refunded
// webhook and the reconciler; every step is idempotent.
func (c *Core) syncPaymentRefunds(params *entity.CheckoutParams, skipRefundId string) (*refundSnapshot, error) {
	defer c.lockPayment(params.PaymentId)()
	return c.syncLocked(params, skipRefundId)
}

// syncLocked is syncPaymentRefunds for a caller already holding the payment lock.
func (c *Core) syncLocked(params *entity.CheckoutParams, skipRefundId string) (*refundSnapshot, error) {
	st, err := c.sc.PaymentRefunds(params.PaymentId)
	if err != nil {
		return nil, err
	}
	snap := &refundSnapshot{received: st.AmountReceived, refunded: st.AmountRefunded, currency: st.Currency}

	for _, r := range st.Refunds {
		r.OrderId = params.OrderId
		r.SessionId = params.SessionId
		inserted, err := c.refundDb.SaveRefund(r)
		if err != nil {
			c.log.With(sl.Err(err), slog.String("refund_id", r.RefundId)).Error("save refund")
			continue
		}
		if inserted && r.RefundId != skipRefundId {
			c.notifyRefund(params, r, snap)
		}
	}

	// Written whenever the payment has refunds, so a refund that later fails or is
	// canceled brings the status back to paid.
	if len(st.Refunds) > 0 && c.oc != nil && params.OrderId != "" {
		status := entity.RefundState(snap.received, snap.refunded)
		if err := c.oc.SavePaymentData(params.OrderId, params.PaymentId, params.SessionId, status, snap.received); err != nil {
			c.log.With(sl.Err(err), slog.String("order_id", params.OrderId)).Error("update payment status after refund")
		}
	}
	return snap, nil
}

// refundSnapshot is the live refund totals of a payment in its currency's minor units.
type refundSnapshot struct {
	received int64
	refunded int64
	currency string
}

// notifyRefund reports a refund to Telegram. The wFirma side is left to a human: whether
// a refund gets a correcting invoice (faktura korygująca) automatically is not decided,
// so the message asks for it to be issued manually when the order was invoiced.
func (c *Core) notifyRefund(params *entity.CheckoutParams, refund *entity.Refund, snap *refundSnapshot) {
	log := c.log.With(
		slog.String("order_id", params.OrderId),
		slog.String("payment_id", params.PaymentId),
		slog.String("refund_id", refund.RefundId),
		slog.String("amount", formatMinor(refund.Amount)),
		slog.String("currency", refund.Currency),
		slog.String("refund_status", refund.Status),
		slog.String("source", refundSource(refund)),
	)
	if refund.Reason != "" {
		log = log.With(slog.String("reason", refund.Reason))
	}
	if refund.RequestedBy != "" {
		log = log.With(slog.String("requested_by", refund.RequestedBy))
	}
	if snap != nil {
		log = log.With(
			slog.String("refunded_total", formatMinor(snap.refunded)),
			slog.String("captured", formatMinor(snap.received)),
			slog.String("payment_status", entity.RefundState(snap.received, snap.refunded)),
		)
	}
	if params.InvoiceId != "" {
		log.With(
			slog.String("invoice_id", params.InvoiceId),
			slog.String("tg_topic", entity.TopicError),
		).Warn("payment refunded: wFirma correcting invoice not issued, issue it manually")
		return
	}
	log.With(slog.String("tg_topic", entity.TopicPayment)).Info("payment refunded")
}

func refundSource(r *entity.Refund) string {
	if r.Source != "" {
		return r.Source
	}
	return entity.RefundSourceStripe
}

// syncRefundsByPayment syncs the refunds of a PaymentIntent known only by id (webhook,
// reconciler). A PaymentIntent with no checkout params is not ours and is ignored.
func (c *Core) syncRefundsByPayment(paymentId string) error {
	if c.sc == nil || c.refundDb == nil || paymentId == "" {
		return nil
	}
	params, err := c.refundDb.GetCheckoutParamsByPayment(paymentId)
	if err != nil {
		return fmt.Errorf("get checkout params: %w", err)
	}
	if params == nil {
		c.log.Debug("refund of unknown payment intent, ignoring", slog.String("payment_id", paymentId))
		return nil
	}
	_, err = c.syncPaymentRefunds(params, "")
	return err
}

// settledStatus is the wf_payment_status for a captured payment: paid, unless refunds
// are already recorded for it, so a late "paid" write (reconciler, a retried
// payment_intent.succeeded webhook) cannot hide a refund.
func (c *Core) settledStatus(params *entity.CheckoutParams) string {
	if c.refundDb == nil || params.PaymentId == "" {
		return entity.PaymentStatusPaid
	}
	refunds, err := c.refundDb.GetRefundsByPayment(params.PaymentId)
	if err != nil {
		return entity.PaymentStatusPaid
	}
	var refunded int64
	for _, r := range refunds {
		if r.Counts() {
			refunded += r.Amount
		}
	}
	return entity.RefundState(received(params), refunded)
}

// attachRefunds adds the stored refunds of a payment to its status report.
func (c *Core) attachRefunds(st *entity.PaymentStatus) {
	if c.refundDb == nil || st.PaymentId == "" {
		return
	}
	refunds, err := c.refundDb.GetRefundsByPayment(st.PaymentId)
	if err != nil {
		c.log.With(sl.Err(err), slog.String("payment_id", st.PaymentId)).Warn("get refunds for status")
		return
	}
	st.Refunds = refunds
}
