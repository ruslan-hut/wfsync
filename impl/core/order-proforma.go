package core

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"time"
	"wfsync/entity"
	"wfsync/lib/sl"
)

// WFirmaOrderFileProforma issues the proforma for an OpenCart order, or returns the one
// already issued when nothing printed on it has changed. The site calls it synchronously
// at checkout to attach the PDF to the order confirmation, so a repeated call must never
// put a new number on a document the customer may already hold.
//
// The issued set is remembered the same way as for B2B (ProformaRecord keyed by the
// order id). The work is detached from the request and serialized per order: when the
// site gives up waiting, issuance still completes and is recorded, and the next call
// returns that set instead of issuing a second one.
//
// The OpenCart status job (WFirmaRegisterProforma) keeps re-issuing on every run: a
// manager moving an order to the proforma status asks for a new document.
func (c *Core) WFirmaOrderFileProforma(ctx context.Context, orderId int64) (*entity.Payment, error) {
	if c.inv == nil {
		return nil, fmt.Errorf("invoice service not connected")
	}
	if c.oc == nil {
		return nil, fmt.Errorf("opencart service not connected")
	}

	ctx = context.WithoutCancel(ctx)
	defer c.proformaLocks.Lock(strconv.FormatInt(orderId, 10))()

	start := time.Now()
	params, err := c.oc.GetOrder(orderId)
	if err != nil {
		return nil, err
	}
	if params == nil {
		return nil, fmt.Errorf("order not found")
	}

	payment, err := c.orderProforma(ctx, params)
	if err != nil {
		return nil, err
	}
	if err := c.oc.UpdateOrderWithProforma(orderId, payment.Id, payment.InvoiceFile); err != nil {
		c.log.With(slog.Int64("order_id", orderId), sl.Err(err)).Warn("save proforma in opencart")
	}

	c.log.With(
		slog.Int64("order_id", orderId),
		slog.String("proforma_id", payment.Id),
		slog.String("number", payment.Number),
		slog.Int64("duration_ms", time.Since(start).Milliseconds()),
		slog.Int("parts", max(len(payment.Parts), 1)),
		slog.Bool("reused", payment.Reused),
	).Info("order proforma ready")
	return payment, nil
}

// orderProforma reuses or issues the proforma set for an OpenCart order read from the
// database. Reuse needs the record to match both the order data and the proforma the order
// points at: a proforma issued since by the status job or by hand replaced the recorded
// one. A proforma the order carries without a record (issued before records were kept, or
// by the status job) has no fingerprint to compare, so it is replaced as before.
func (c *Core) orderProforma(ctx context.Context, params *entity.CheckoutParams) (*entity.Payment, error) {
	ref := params.ExternalRef()
	log := c.log.With(slog.String("order_id", params.OrderId))
	// The customer group decides the VAT treatment of a shop order, so a change of it alone
	// must re-issue. Kept out of ProformaFingerprint: that would change every recorded B2B
	// fingerprint and re-issue those sets on their next request.
	fingerprint := params.ProformaFingerprint() + ":" + strconv.Itoa(params.CustomerGroup)

	record, err := c.proformaRecord(ref)
	if err != nil {
		return nil, fmt.Errorf("read proforma record: %w", err)
	}

	if record != nil && record.Fingerprint == fingerprint &&
		(params.ProformaId == "" || slices.Contains(record.DocumentIds(), params.ProformaId)) {
		payment, err := c.reuseRecorded(ctx, record, log)
		if err != nil || payment != nil {
			return payment, err
		}
	}

	if err := c.discardProformas(ctx, ref, record, log); err != nil {
		return nil, err
	}
	c.discardExistingProforma(ctx, params)

	return c.issueProforma(ctx, params, ref, fingerprint, log)
}
