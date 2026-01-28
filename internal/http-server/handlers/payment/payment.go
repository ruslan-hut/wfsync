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
	StripeCaptureAmount(sessionId string, amount int64) (*entity.Payment, error)
	StripeCancelPayment(sessionId, reason string) (*entity.Payment, error)
	StripePayAmount(ctx context.Context, params *entity.CheckoutParams) (*entity.Payment, error)
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
			logger.Error("validate total", sl.Err(err))
			render.Status(r, 400)
			render.JSON(w, r, response.Error(fmt.Sprintf("Invalid total: %v", err)))
			return
		}
		logger = logger.With(
			slog.Int("items_count", len(checkoutParams.LineItems)),
			slog.Int64("total", checkoutParams.Total),
		)
		checkoutParams.Source = entity.SourceApi

		pm, err := handler.StripeHoldAmount(&checkoutParams)
		if err != nil {
			logger.Error("hold amount", sl.Err(err))
			render.Status(r, 400)
			render.JSON(w, r, response.Error(fmt.Sprintf("Get link: %v", err)))
			return
		}
		logger.Debug("payment link created")

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
		logger = logger.With(
			slog.Int64("amount", checkoutParams.Total),
		)

		pm, err := handler.StripeCaptureAmount(id, checkoutParams.Total)
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
			slog.String("payment_id", id),
			slog.String("reason", reason),
		)

		if handler == nil {
			logger.Error("stripe service not available")
			render.JSON(w, r, response.Error("Stripe service not available"))
			return
		}

		pm, err := handler.StripeCancelPayment(id, reason)
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
		)
		checkoutParams.Source = entity.SourceApi

		pm, err := handler.StripePayAmount(r.Context(), &checkoutParams)
		if err != nil {
			logger.Error("pay amount", sl.Err(err))
			render.Status(r, 400)
			render.JSON(w, r, response.Error(fmt.Sprintf("Get link: %v", err)))
			return
		}
		logger.Debug("payment link created")

		render.JSON(w, r, response.Ok(pm))
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
