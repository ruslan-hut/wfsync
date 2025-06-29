package payment

import (
	"fmt"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/render"
	"log/slog"
	"net/http"
	"wfsync/entity"
	"wfsync/lib/api/response"
	"wfsync/lib/sl"
)

type Core interface {
	StripePaymentLink(params *entity.CheckoutParams) (*entity.Payment, error)
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
		logger = logger.With(
			slog.Int("items_count", len(checkoutParams.LineItems)),
			slog.Int64("total", checkoutParams.Total),
		)

		pm, err := handler.StripePaymentLink(&checkoutParams)
		if err != nil {
			logger.Error("get payment link", sl.Err(err))
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

		logger := log.With(
			mod,
			slog.String("request_id", middleware.GetReqID(r.Context())),
		)

		if handler == nil {
			logger.Error("stripe service not available")
			render.JSON(w, r, response.Error("Stripe service not available"))
			return
		}

		var payment entity.Payment
		if err := render.Bind(r, &payment); err != nil {
			logger.Error("bind request", sl.Err(err))
			render.Status(r, 400)
			render.JSON(w, r, response.Error(fmt.Sprintf("Invalid request: %v", err)))
			return
		}
		logger = logger.With(
			slog.String("id", payment.Id),
			slog.Int64("amount", payment.Amount),
		)

		//pm, err := handler.StripePaymentLink(&checkoutParams)
		//if err != nil {
		//	logger.Error("get payment link", sl.Err(err))
		//	render.Status(r, 400)
		//	render.JSON(w, r, response.Error(fmt.Sprintf("Get link: %v", err)))
		//	return
		//}
		logger.Debug("payment captured")

		render.JSON(w, r, response.Ok(nil))
	}
}

func Cancel(log *slog.Logger, handler Core) http.HandlerFunc {
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

		var payment entity.Payment
		if err := render.Bind(r, &payment); err != nil {
			logger.Error("bind request", sl.Err(err))
			render.Status(r, 400)
			render.JSON(w, r, response.Error(fmt.Sprintf("Invalid request: %v", err)))
			return
		}
		logger = logger.With(
			slog.String("id", payment.Id),
		)

		//pm, err := handler.StripePaymentLink(&checkoutParams)
		//if err != nil {
		//	logger.Error("get payment link", sl.Err(err))
		//	render.Status(r, 400)
		//	render.JSON(w, r, response.Error(fmt.Sprintf("Get link: %v", err)))
		//	return
		//}
		logger.Debug("payment canceled")

		render.JSON(w, r, response.Ok(nil))
	}
}
