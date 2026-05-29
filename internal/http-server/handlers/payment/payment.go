package payment

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"wfsync/entity"
	"wfsync/lib/api/response"
	"wfsync/lib/sl"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/render"
)

type Core interface {
	StripeHoldAmount(params *entity.CheckoutParams) (*entity.Payment, error)
	StripeCaptureAmount(sessionId string, amount int64) (*entity.Payment, *entity.CheckoutParams, error)
	StripeCancelPayment(sessionId, reason string) (*entity.Payment, *entity.CheckoutParams, error)
	StripePayAmount(ctx context.Context, params *entity.CheckoutParams) (*entity.Payment, error)
	StripePaymentStatus(orderId string) (*entity.PaymentStatus, error)
	ReconcileQueue() ([]*entity.HeldPaymentSummary, error)
}

func Hold(log *slog.Logger, handler Core) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mod := sl.Module("http.handlers.payment")

		logger := log.With(
			mod,
			slog.String("request_id", middleware.GetReqID(r.Context())),
		)

		if handler == nil {
			logger.Error("stripe service not available")
			render.JSON(w, r, response.Error("Stripe service not available"))
			return
		}

		var checkoutParams entity.CheckoutParams
		if err := render.Bind(r, &checkoutParams); err != nil {
			logger.Error("bind request", sl.Err(err))
			render.Status(r, 400)
			render.JSON(w, r, response.Error(fmt.Sprintf("Invalid request: %v", err)))
			return
		}
		if err := checkoutParams.ValidateTotal(); err != nil {
			// Tolerate discount/rounding gaps: redistribute line items to match
			// the authoritative total instead of rejecting the request.
			logger.Warn("total mismatch, recalculating", sl.Err(err))
			checkoutParams.RecalcWithDiscount()
		}
		logger = logger.With(
			slog.Int("items_count", len(checkoutParams.LineItems)),
			slog.Int64("total", checkoutParams.Total),
			slog.String("order_id", checkoutParams.OrderId),
		)
		if checkoutParams.ClientDetails != nil {
			logger = logger.With(slog.String("client_name", checkoutParams.ClientDetails.Name))
		}
		checkoutParams.Source = entity.SourceApi

		pm, err := handler.StripeHoldAmount(&checkoutParams)
		if err != nil {
			logger.Error("hold amount", sl.Err(err))
			render.Status(r, 400)
			render.JSON(w, r, response.Error(fmt.Sprintf("Get link: %v", err)))
			return
		}
		// session_id links this order to its Stripe session for later capture/cancel tracking.
		logger.With(slog.String("session_id", pm.Id)).Debug("payment link created")

		render.JSON(w, r, response.Ok(pm))
	}
}

func Capture(log *slog.Logger, handler Core) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mod := sl.Module("http.handlers.payment")
		id := chi.URLParam(r, "id")

		logger := log.With(
			mod,
			slog.String("request_id", middleware.GetReqID(r.Context())),
			slog.String("session_id", id),
		)

		//amountStr := r.URL.Query().Get("amount")
		//var amount int64
		//if amountStr != "" {
		//	v, err := strconv.ParseInt(amountStr, 10, 64)
		//	if err != nil || v < 0 {
		//		render.Status(r, 400)
		//		render.JSON(w, r, response.Error(fmt.Sprintf("Invalid amount: %v", err)))
		//		return
		//	}
		//	amount = v
		//}

		if handler == nil {
			logger.Error("stripe service not available")
			render.JSON(w, r, response.Error("Stripe service not available"))
			return
		}

		var checkoutParams entity.CheckoutParams
		if err := render.Bind(r, &checkoutParams); err != nil {
			logger.Error("bind request", sl.Err(err))
			render.Status(r, 400)
			render.JSON(w, r, response.Error(fmt.Sprintf("Invalid request: %v", err)))
			return
		}
		logger = logger.With(slog.Int64("amount", checkoutParams.Total))
		if checkoutParams.ClientDetails != nil {
			logger = logger.With(slog.String("client_name", checkoutParams.ClientDetails.Name))
		}

		pm, params, err := handler.StripeCaptureAmount(id, checkoutParams.Total)
		// Prefer the order id resolved from the held session over the request body, which
		// may carry an unrelated external reference (e.g. a Zoho id).
		if params != nil && params.OrderId != "" {
			logger = logger.With(slog.String("order_id", params.OrderId))
		} else {
			logger = logger.With(slog.String("order_id", checkoutParams.OrderId))
		}
		if err != nil {
			logger.Error("capture amount", sl.Err(err))
			render.Status(r, 400)
			render.JSON(w, r, response.Error(fmt.Sprintf("Capture: %v", err)))
			return
		}
		logger.Debug("amount captured")

		render.JSON(w, r, response.Ok(pm))
	}
}

func Cancel(log *slog.Logger, handler Core) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mod := sl.Module("http.handlers.payment")
		id := chi.URLParam(r, "id")
		reason := r.URL.Query().Get("reason")

		if !isValidReason(reason) {
			render.Status(r, 400)
			render.JSON(w, r, response.Error("Invalid reason"))
			return
		}

		logger := log.With(
			mod,
			slog.String("request_id", middleware.GetReqID(r.Context())),
			slog.String("session_id", id),
			slog.String("reason", reason),
		)

		if handler == nil {
			logger.Error("stripe service not available")
			render.JSON(w, r, response.Error("Stripe service not available"))
			return
		}

		pm, params, err := handler.StripeCancelPayment(id, reason)
		// Log the OpenCart order id resolved from the held session so the cancel event can
		// be tracked alongside the matching hold/capture events.
		if params != nil && params.OrderId != "" {
			logger = logger.With(slog.String("order_id", params.OrderId))
		}
		if err != nil {
			logger.Error("cancel payment", sl.Err(err))
			render.Status(r, 400)
			render.JSON(w, r, response.Error(fmt.Sprintf("Cancel payment: %v", err)))
			return
		}
		logger.Debug("payment canceled")

		render.JSON(w, r, response.Ok(pm))
	}
}

func Pay(log *slog.Logger, handler Core) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mod := sl.Module("http.handlers.payment")

		logger := log.With(
			mod,
			slog.String("request_id", middleware.GetReqID(r.Context())),
		)

		if handler == nil {
			logger.Error("stripe service not available")
			render.JSON(w, r, response.Error("Stripe service not available"))
			return
		}

		var checkoutParams entity.CheckoutParams
		if err := render.Bind(r, &checkoutParams); err != nil {
			logger.Error("bind request", sl.Err(err))
			render.Status(r, 400)
			render.JSON(w, r, response.Error(fmt.Sprintf("Invalid request: %v", err)))
			return
		}
		if err := checkoutParams.ValidateTotal(); err != nil {
			logger.Error("validate total", sl.Err(err))
			render.Status(r, 400)
			render.JSON(w, r, response.Error(fmt.Sprintf("Invalid total: %v", err)))
			return
		}
		logger = logger.With(
			slog.Int("items_count", len(checkoutParams.LineItems)),
			slog.Int64("total", checkoutParams.Total),
			slog.String("order_id", checkoutParams.OrderId),
		)
		checkoutParams.Source = entity.SourceApi

		pm, err := handler.StripePayAmount(r.Context(), &checkoutParams)
		if err != nil {
			logger.Error("pay amount", sl.Err(err))
			render.Status(r, 400)
			render.JSON(w, r, response.Error(fmt.Sprintf("Get link: %v", err)))
			return
		}
		logger.With(slog.String("session_id", pm.Id)).Debug("payment link created")

		render.JSON(w, r, response.Ok(pm))
	}
}

// Status reports the live Stripe payment state for an OpenCart order id.
func Status(log *slog.Logger, handler Core) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mod := sl.Module("http.handlers.payment")
		id := chi.URLParam(r, "id")

		logger := log.With(
			mod,
			slog.String("request_id", middleware.GetReqID(r.Context())),
			slog.String("order_id", id),
		)

		if handler == nil {
			logger.Error("stripe service not available")
			render.JSON(w, r, response.Error("Stripe service not available"))
			return
		}

		st, err := handler.StripePaymentStatus(id)
		if err != nil {
			logger.Error("payment status", sl.Err(err))
			render.Status(r, 400)
			render.JSON(w, r, response.Error(fmt.Sprintf("Payment status: %v", err)))
			return
		}
		logger.Debug("payment status", slog.String("status", st.Status))

		render.JSON(w, r, response.Ok(st))
	}
}

// Queue lists the held payments currently awaiting reconciliation (have a PaymentIntent
// but no invoice yet). Useful for inspecting the reconciler backlog without scraping logs.
func Queue(log *slog.Logger, handler Core) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mod := sl.Module("http.handlers.payment")

		logger := log.With(
			mod,
			slog.String("request_id", middleware.GetReqID(r.Context())),
		)

		if handler == nil {
			logger.Error("stripe service not available")
			render.JSON(w, r, response.Error("Stripe service not available"))
			return
		}

		items, err := handler.ReconcileQueue()
		if err != nil {
			logger.Error("reconcile queue", sl.Err(err))
			render.Status(r, 400)
			render.JSON(w, r, response.Error(fmt.Sprintf("Queue: %v", err)))
			return
		}
		logger.Debug("reconcile queue", slog.Int("count", len(items)))

		render.JSON(w, r, response.Ok(items))
	}
}

func isValidReason(s string) bool {
	if len(s) > 255 {
		return false
	}
	for _, r := range s {
		// Запрещаем управляющие символы (всё до пробела, кроме \n и \t)
		if r < 32 && r != '\n' && r != '\t' {
			return false
		}
	}
	return true
}
