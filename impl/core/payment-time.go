// Package core — payment-time.go fills oc_order.wf_payment_time for orders whose
// payment became final (paid/canceled) before the column existed. New payments get the
// time from UpdatePayment itself; this backfill only covers the history so an external
// reader (1C via ocapi) can load past captures by time.
package core

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"wfsync/entity"
	"wfsync/lib/sl"
)

const (
	// paymentTimeBackfillDays bounds the backfill to recent orders: older payments are
	// settled in the books already and are not meant to be loaded again.
	paymentTimeBackfillDays = 90
	// paymentTimeBackfillLimit caps one run; the whole window fits in it with a margin.
	paymentTimeBackfillLimit = 5000
)

// paymentTimeStore is the OpenCart side of the backfill.
type paymentTimeStore interface {
	OrdersMissingPaymentTime(since time.Time, limit int) ([]int64, error)
	SetPaymentTimeIfEmpty(orderId int64, at time.Time) error
}

// paymentRecordSource looks up the local checkout record of an order.
type paymentRecordSource interface {
	GetCheckoutParamsByOrder(orderId string) (*entity.CheckoutParams, error)
}

// paymentFinalTime picks the best known moment a payment became final from its local
// record. closed is stamped when the order is invoiced (paid) or the reconciler reflects
// a cancellation, i.e. right after the outcome; modified is the last write and is used
// only when closed is missing. Zero means the record carries no usable time.
func paymentFinalTime(p *entity.CheckoutParams) time.Time {
	if p == nil {
		return time.Time{}
	}
	if !p.Closed.IsZero() {
		return p.Closed
	}
	return p.Modified
}

// backfillPaymentTime stamps wf_payment_time on final orders added since the given
// moment, taking the time from the local checkout record. Orders without a record or
// with no usable time are left empty and counted as missing; they are not exported by
// time, which is the safe outcome for history nobody can date.
func backfillPaymentTime(ctx context.Context, log *slog.Logger, store paymentTimeStore, src paymentRecordSource, since time.Time) (filled, missing int, err error) {
	ids, err := store.OrdersMissingPaymentTime(since, paymentTimeBackfillLimit)
	if err != nil {
		return 0, 0, err
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return filled, missing, ctx.Err()
		}
		orderId := strconv.FormatInt(id, 10)
		params, err := src.GetCheckoutParamsByOrder(orderId)
		if err != nil {
			log.Warn("payment time backfill: read checkout record", slog.String("order_id", orderId), sl.Err(err))
			missing++
			continue
		}
		at := paymentFinalTime(params)
		if at.IsZero() {
			missing++
			continue
		}
		if err = store.SetPaymentTimeIfEmpty(id, at); err != nil {
			log.Warn("payment time backfill: write", slog.String("order_id", orderId), sl.Err(err))
			missing++
			continue
		}
		filled++
	}
	return filled, missing, nil
}

// BackfillPaymentTime runs the one-off wf_payment_time backfill over the last
// paymentTimeBackfillDays days. It is idempotent (only empty times are written) and
// cheap once done, so it is safe to run on every start.
func (c *Core) BackfillPaymentTime(ctx context.Context) {
	if c.oc == nil || c.db == nil {
		return
	}
	since := time.Now().AddDate(0, 0, -paymentTimeBackfillDays)
	filled, missing, err := backfillPaymentTime(ctx, c.log, c.oc, c.db, since)
	if err != nil {
		c.log.Error("payment time backfill", sl.Err(err))
		return
	}
	if filled > 0 || missing > 0 {
		c.log.Info("payment time backfill",
			slog.Int("filled", filled),
			slog.Int("missing", missing))
	}
}
