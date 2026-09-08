package b2b

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"wfsync/entity"
	"wfsync/lib/api/response"
	"wfsync/lib/sl"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/render"
)

// statusBatchLimit caps one batch request. The portal syncs the orders it is showing,
// not its whole history.
const statusBatchLimit = 500

// PaymentStatusCore is the settlement lookup the portal needs.
type PaymentStatusCore interface {
	PaymentFacts(refs []string) ([]*entity.PaymentFact, error)
}

// orderPaymentStatus is what the portal shows a customer about their order.
//
// Paid is deliberately narrower than Status: a payment tied to a document only by amount
// and counterparty is a guess, and telling a customer their order is paid on a guess is
// worse than telling them nothing. Such an order reports Status "partial" with Paid
// false until a person confirms it.
type orderPaymentStatus struct {
	OrderUID string `json:"order_uid"`
	// Paid is the single field a portal needs to decide what to show. It is true only
	// for a settled order matched by a stated document number or confirmed by hand.
	Paid   bool   `json:"paid"`
	Status string `json:"status"`
	// AmountStatus explains a sum that does not equal the document: a partial payment,
	// an overpayment, or a payment made in another currency.
	AmountStatus   string `json:"amount_status,omitempty"`
	AmountPaid     int64  `json:"amount_paid"`
	CurrencyPaid   string `json:"currency_paid,omitempty"`
	AmountDue      int64  `json:"amount_due,omitempty"`
	CurrencyDoc    string `json:"currency_doc,omitempty"`
	DocumentNumber string `json:"document_number,omitempty"`
	PaidAt         string `json:"paid_at,omitempty"`
	MatchLevel     string `json:"match_level,omitempty"`
	UpdatedAt      string `json:"updated_at,omitempty"`
}

// unpaidStatus is the answer for an order no payment has arrived for. An order the
// portal asks about but that has no record is not an error: it simply has not been paid.
func unpaidStatus(ref string) orderPaymentStatus {
	return orderPaymentStatus{OrderUID: ref, Paid: false, Status: entity.PaymentFactUnpaid}
}

func toStatus(f *entity.PaymentFact) orderPaymentStatus {
	s := orderPaymentStatus{
		OrderUID:       f.ExternalRef,
		Paid:           f.Settled(),
		Status:         f.Status,
		AmountStatus:   f.AmountStatus,
		AmountPaid:     f.AmountPaid,
		CurrencyPaid:   f.CurrencyPaid,
		AmountDue:      f.AmountDue,
		CurrencyDoc:    f.CurrencyDoc,
		DocumentNumber: f.DocumentNumber,
		MatchLevel:     f.MatchLevel,
	}
	if !f.PaidAt.IsZero() {
		s.PaidAt = f.PaidAt.Format(time.DateOnly)
	}
	if !f.UpdatedAt.IsZero() {
		// The portal polls; UpdatedAt lets it pull only what changed.
		s.UpdatedAt = f.UpdatedAt.Format(time.RFC3339)
	}
	return s
}

// PaymentStatus handles GET /v1/b2b/status/{order_uid} — has this order been paid?
func PaymentStatus(logger *slog.Logger, handler PaymentStatusCore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		log := logger.With(
			sl.Module("http.handlers.b2b"),
			slog.String("request_id", middleware.GetReqID(r.Context())),
		)

		if handler == nil {
			render.Status(r, http.StatusServiceUnavailable)
			render.JSON(w, r, response.Error("Payment status not available"))
			return
		}

		ref := chi.URLParam(r, "order_uid")
		if ref == "" {
			render.Status(r, http.StatusBadRequest)
			render.JSON(w, r, response.Error("order_uid is required"))
			return
		}

		facts, err := handler.PaymentFacts([]string{ref})
		if err != nil {
			log.Error("payment facts", sl.Err(err))
			render.JSON(w, r, response.Error(fmt.Sprintf("Payment status: %v", err)))
			return
		}
		if len(facts) == 0 {
			render.JSON(w, r, response.Ok(unpaidStatus(ref)))
			return
		}
		render.JSON(w, r, response.Ok(toStatus(facts[0])))
	}
}

// statusBatchRequest asks about many orders at once.
type statusBatchRequest struct {
	OrderUIDs []string `json:"order_uids"`
}

func (b *statusBatchRequest) Bind(_ *http.Request) error {
	if len(b.OrderUIDs) == 0 {
		return fmt.Errorf("order_uids is required")
	}
	if len(b.OrderUIDs) > statusBatchLimit {
		return fmt.Errorf("order_uids exceeds %d entries", statusBatchLimit)
	}
	return nil
}

// PaymentStatusBatch handles POST /v1/b2b/status — payment status for many orders in one
// request.
//
// It exists so the portal does not walk its order list one HTTP call at a time. Every
// requested order gets an entry, including those with no payment on record, so the caller
// can tell "not paid" from "not asked about" without comparing lists.
func PaymentStatusBatch(logger *slog.Logger, handler PaymentStatusCore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		log := logger.With(
			sl.Module("http.handlers.b2b"),
			slog.String("request_id", middleware.GetReqID(r.Context())),
		)

		if handler == nil {
			render.Status(r, http.StatusServiceUnavailable)
			render.JSON(w, r, response.Error("Payment status not available"))
			return
		}

		var req statusBatchRequest
		if err := render.Bind(r, &req); err != nil {
			render.Status(r, http.StatusBadRequest)
			render.JSON(w, r, response.Error(fmt.Sprintf("Invalid request: %v", err)))
			return
		}

		facts, err := handler.PaymentFacts(req.OrderUIDs)
		if err != nil {
			log.Error("payment facts", sl.Err(err))
			render.JSON(w, r, response.Error(fmt.Sprintf("Payment status: %v", err)))
			return
		}

		byRef := make(map[string]*entity.PaymentFact, len(facts))
		for _, f := range facts {
			byRef[f.ExternalRef] = f
		}

		items := make([]orderPaymentStatus, 0, len(req.OrderUIDs))
		for _, ref := range req.OrderUIDs {
			if f, ok := byRef[ref]; ok {
				items = append(items, toStatus(f))
				continue
			}
			items = append(items, unpaidStatus(ref))
		}

		render.JSON(w, r, response.Ok(statusBatchResponse{Count: len(items), Items: items}))
	}
}

type statusBatchResponse struct {
	Count int                  `json:"count"`
	Items []orderPaymentStatus `json:"items"`
}
