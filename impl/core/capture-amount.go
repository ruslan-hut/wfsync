package core

import (
	"fmt"
	"log/slog"
	"sync"
	"wfsync/entity"
	"wfsync/lib/sl"
)

// captureAmountTolerance is the largest invoice-vs-capture difference, in minor units,
// still treated as rounding.
const captureAmountTolerance = 1

// errCaptureAmountMismatch marks an invoice blocked because its gross differs from what
// Stripe actually charged. It is terminal: retrying cannot fix it, a human must.
var errCaptureAmountMismatch = fmt.Errorf("invoice total does not match captured amount")

// checkCapturedAmount compares the invoice gross (params.Total, which processInvoice has
// already replaced with the OpenCart total) with the amount Stripe captured. The invoice
// is built from OpenCart, while the hold link can carry a total edited in Zoho (e.g. a
// manager's per-line discount, order 18234), so the two can drift apart. AmountPaid == 0
// means the captured amount is unknown (direct payments, legacy records) and is not checked.
func checkCapturedAmount(params *entity.CheckoutParams) error {
	if params.AmountPaid <= 0 {
		return nil
	}
	diff := params.Total - params.AmountPaid
	if diff < 0 {
		diff = -diff
	}
	if diff <= captureAmountTolerance {
		return nil
	}
	return fmt.Errorf("%w: invoice %s, captured %s", errCaptureAmountMismatch,
		formatMinor(params.Total), formatMinor(params.AmountPaid))
}

// amountAlerts remembers which mismatches were already reported, so the capture API,
// the payment_intent.succeeded webhook and the reconciler — which all observe the same
// capture — raise one Telegram signal per order rather than three.
type amountAlerts struct {
	seen sync.Map
}

// first reports whether this order/amount combination is being reported for the first time.
func (a *amountAlerts) first(params *entity.CheckoutParams) bool {
	key := fmt.Sprintf("%s:%d:%d", params.OrderId, params.Total, params.AmountPaid)
	_, loaded := a.seen.LoadOrStore(key, struct{}{})
	return !loaded
}

// reportAmountMismatch logs a blocked invoice; only the first report per order goes to
// Telegram, repeats stay in the log.
func (c *Core) reportAmountMismatch(params *entity.CheckoutParams, err error) {
	log := c.log.With(
		slog.String("order_id", params.OrderId),
		slog.String("payment_id", params.PaymentId),
		slog.String("event_id", params.EventId),
		slog.String("invoice_total", formatMinor(params.Total)),
		slog.String("captured", formatMinor(params.AmountPaid)),
		slog.String("currency", params.Currency),
		sl.Err(err),
	)
	if c.amountAlerts == nil || c.amountAlerts.first(params) {
		log = log.With(slog.String("tg_topic", entity.TopicError))
	}
	log.Error("invoice not issued: total differs from captured amount, issue it manually")
}

func formatMinor(v int64) string {
	sign := ""
	if v < 0 {
		sign, v = "-", -v
	}
	return fmt.Sprintf("%s%d.%02d", sign, v/100, v%100)
}
