// Package core — reconciler.go implements a periodic reconciliation job that compares
// the locally stored state of held Stripe payments against their live status in Stripe.
// It exists because a manual capture emits no Stripe webhook the service handles, so a
// captured (succeeded) PaymentIntent can otherwise be left without a wFirma invoice.
//
// Policy (see decisions captured for this job):
//   - succeeded, not yet invoiced -> request the wFirma invoice (re-using the shared
//     invoice path, with retry-queue fallback) and close the record.
//   - canceled (manually, or Stripe's ~7-day auto-cancel of an uncaptured auth) ->
//     reflect the cancellation into OpenCart and close the record.
//   - requires_capture -> an active hold awaiting a manual capture decision; never
//     auto-canceled, only watched (logged, no Telegram).
//   - any other non-terminal status -> left pending and re-checked next tick.
//
// Telegram notifications are emitted only when the job takes an action (invoice
// requested or cancellation reflected), never for idle/no-op ticks.
package core

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"wfsync/entity"
	"wfsync/internal/stripeclient"
	"wfsync/lib/sl"

	"github.com/stripe/stripe-go/v76"
)

// reconcileBatchLimit caps how many unresolved holds are pulled per tick so a backlog
// cannot trigger an unbounded burst of Stripe calls in a single run.
const reconcileBatchLimit = 200

// ReconcileDatabase defines the persistence methods the reconciler needs.
type ReconcileDatabase interface {
	GetUnresolvedHeldParams(limit int) ([]*entity.CheckoutParams, error)
	CloseCheckoutParams(paymentId, invoiceId string) error
}

// reconcileOutcome classifies what happened to a single held payment in one pass,
// used to build the first-run backlog summary.
type reconcileOutcome int

const (
	outcomePending         reconcileOutcome = iota // not settled, active hold, or transient error — left open
	outcomeInvoiced                                // captured and a new invoice was requested
	outcomeAlreadyInvoiced                         // captured but already invoiced elsewhere — just closed
	outcomeCanceled                                // cancellation reflected and closed
	outcomeSkipped                                 // could not act (e.g. invalid order id, OpenCart missing)
)

// Reconciler periodically inspects unresolved held payments and reconciles them with
// live Stripe state. Follows the same Start/Stop pattern as RetryQueue.
type Reconciler struct {
	core     *Core
	db       ReconcileDatabase
	log      *slog.Logger
	interval time.Duration
	firstRun bool
	done     chan struct{}
	stopped  chan struct{}
}

// NewReconciler creates a reconciler. Call Start() to begin background processing.
func NewReconciler(core *Core, log *slog.Logger, intervalMin int) *Reconciler {
	if intervalMin <= 0 {
		intervalMin = 15
	}
	return &Reconciler{
		core:     core,
		log:      log.With(sl.Module("reconciler")),
		interval: time.Duration(intervalMin) * time.Minute,
		firstRun: true,
	}
}

func (r *Reconciler) SetDatabase(db ReconcileDatabase) { r.db = db }

// Start launches the background polling goroutine.
func (r *Reconciler) Start() {
	r.done = make(chan struct{})
	r.stopped = make(chan struct{})
	go func() {
		defer close(r.stopped)

		r.reconcile()

		ticker := time.NewTicker(r.interval)
		defer ticker.Stop()
		for {
			select {
			case <-r.done:
				r.log.Debug("reconciler stopped")
				return
			case <-ticker.C:
				r.reconcile()
			}
		}
	}()
}

// Stop signals the background goroutine to exit and waits for it to finish.
func (r *Reconciler) Stop() {
	if r.done != nil {
		r.log.Debug("stopping reconciler")
		close(r.done)
		<-r.stopped
	}
}

// reconcile pulls the current batch of unresolved holds and processes each one.
// On the first run after startup it emits a one-off summary of the backlog it
// processed, so the initial reconciliation of pre-existing holds is visible.
func (r *Reconciler) reconcile() {
	if r.db == nil || r.core == nil || r.core.sc == nil {
		return
	}
	firstRun := r.firstRun
	r.firstRun = false

	params, err := r.db.GetUnresolvedHeldParams(reconcileBatchLimit)
	if err != nil {
		r.log.Error("get unresolved held params", sl.Err(err))
		return
	}
	if len(params) == 0 {
		if firstRun {
			r.log.Info("reconciler first run: no unresolved held payments")
		}
		return
	}

	r.log.Debug("reconciling held payments", slog.Int("count", len(params)))
	var counts [outcomeSkipped + 1]int
	for _, p := range params {
		counts[r.reconcileOne(p)]++
	}

	if firstRun {
		// Log-only summary (no Telegram, per the actions-only notification policy)
		// to show the size and shape of the backlog cleared on startup.
		r.log.Info("reconciler first run summary",
			slog.Int("scanned", len(params)),
			slog.Int("invoiced", counts[outcomeInvoiced]),
			slog.Int("already_invoiced", counts[outcomeAlreadyInvoiced]),
			slog.Int("canceled", counts[outcomeCanceled]),
			slog.Int("pending", counts[outcomePending]),
			slog.Int("skipped", counts[outcomeSkipped]),
		)
	}
}

// reconcileOne fetches the live PaymentIntent status for a single held payment and acts
// according to the reconciliation policy documented at the top of the file. It returns
// the outcome so the caller can tally a first-run summary.
func (r *Reconciler) reconcileOne(params *entity.CheckoutParams) reconcileOutcome {
	log := r.log.With(
		slog.String("order_id", params.OrderId),
		slog.String("payment_id", params.PaymentId),
	)

	status, _, err := r.core.sc.PaymentIntentStatus(params.PaymentId)
	if err != nil {
		if errors.Is(err, stripeclient.ErrPaymentIntentNotFound) {
			// The PaymentIntent does not exist (e.g. stale or test-mode records queried
			// with a live key) — nothing to reconcile, close it so we stop re-checking.
			log.Warn("payment intent not found in stripe, closing record")
			r.closeRecord(log, params, "")
			return outcomeSkipped
		}
		// Transient: leave the record open and retry next tick.
		log.Warn("get payment intent status", sl.Err(err))
		return outcomePending
	}
	log = log.With(slog.String("status", status))

	switch stripe.PaymentIntentStatus(status) {
	case stripe.PaymentIntentStatusSucceeded:
		return r.handleSucceeded(log, params)
	case stripe.PaymentIntentStatusCanceled:
		return r.handleCanceled(log, params)
	case stripe.PaymentIntentStatusRequiresCapture:
		// Active hold awaiting a capture decision — watch only, never auto-cancel.
		return outcomePending
	default:
		// requires_payment_method / requires_confirmation / requires_action / processing
		log.Debug("payment not settled, will re-check")
		return outcomePending
	}
}

// handleSucceeded reconciles a captured payment: mark the order paid, and register the
// wFirma invoice if one does not already exist. Idempotent — an order that already
// carries an invoice is simply closed without re-invoicing.
func (r *Reconciler) handleSucceeded(log *slog.Logger, params *entity.CheckoutParams) reconcileOutcome {
	oc := r.core.oc
	if oc == nil {
		log.Warn("opencart not connected, cannot reconcile captured payment")
		return outcomeSkipped
	}

	orderId, err := parseOrderId(params.OrderId)
	if err != nil {
		// order_id is not numeric — it holds the Stripe session id because the session
		// carried no order_id metadata. Try to recover the real order by reverse-looking
		// up the Stripe payment/session references OpenCart stored on the order.
		orderId, err = oc.OrderIdByPaymentRef(params.PaymentId, params.SessionId)
		if err != nil {
			log.Error("recover order by payment ref", sl.Err(err))
			return outcomePending // transient DB issue, retry next tick
		}
		if orderId == 0 {
			// Genuinely not ours (e.g. a foreign Stripe session) — nothing to invoice.
			// Close the record and surface it once for manual review.
			log.With(slog.String("tg_topic", entity.TopicError)).
				Warn("captured payment has no resolvable opencart order, closing for manual review")
			r.closeRecord(log, params, "")
			return outcomeSkipped
		}
		// Repair the record's order_id so the invoice and OpenCart writes target the
		// recovered order rather than the session id.
		params.OrderId = strconv.FormatInt(orderId, 10)
		log = log.With(slog.String("recovered_order_id", params.OrderId))
		log.Info("recovered opencart order from payment reference")
	}

	if err := oc.SavePaymentData(params.OrderId, params.PaymentId, params.SessionId, "paid", params.Total); err != nil {
		log.Error("update payment status during reconcile", sl.Err(err))
	}

	order, err := oc.GetOrder(orderId)
	if err != nil {
		log.Error("get order during reconcile", sl.Err(err))
		return outcomePending // leave open, retry next tick
	}
	if order != nil && order.InvoiceId != "" {
		// Already invoiced elsewhere (webhook/capture/manual) — just close the record.
		r.closeRecord(log, params, order.InvoiceId)
		return outcomeAlreadyInvoiced
	}

	payment := r.core.processInvoice(context.Background(), params)
	invoiceId := ""
	if payment != nil {
		invoiceId = payment.Id
	}

	// Close the record either way: on success it is invoiced; on failure processInvoice
	// has handed the job to the retry queue, which now owns it.
	r.closeRecord(log, params, invoiceId)

	if payment != nil {
		log.With(
			slog.String("invoice_id", payment.Id),
			slog.String("tg_topic", entity.TopicInvoice),
		).Info("reconciler requested invoice for captured order")
	}
	return outcomeInvoiced
}

// handleCanceled reflects a Stripe-side cancellation (manual or the ~7-day auto-cancel
// of an uncaptured authorization) into OpenCart and closes the record.
func (r *Reconciler) handleCanceled(log *slog.Logger, params *entity.CheckoutParams) reconcileOutcome {
	if r.core.oc != nil {
		if err := r.core.oc.SavePaymentData(params.OrderId, params.PaymentId, params.SessionId, "canceled", params.Total); err != nil {
			log.Error("update canceled status during reconcile", sl.Err(err))
		}
	}
	r.closeRecord(log, params, "")
	log.With(slog.String("tg_topic", entity.TopicPayment)).Info("reconciler reflected canceled hold")
	return outcomeCanceled
}

// closeRecord marks the checkout params as resolved so subsequent ticks skip it. Keyed
// on payment_id, which is always present on reconciled records and is never rewritten by
// the order_id repair — unlike session_id, which can be empty for foreign/legacy records.
func (r *Reconciler) closeRecord(log *slog.Logger, params *entity.CheckoutParams, invoiceId string) {
	if err := r.db.CloseCheckoutParams(params.PaymentId, invoiceId); err != nil {
		log.Error("close reconciled record", sl.Err(err))
	}
}

// parseOrderId converts a stored order id to its numeric OpenCart id, tolerating the
// "test_" prefix applied to records created while Stripe test mode is enabled.
func parseOrderId(orderId string) (int64, error) {
	return strconv.ParseInt(strings.TrimPrefix(orderId, "test_"), 10, 64)
}
