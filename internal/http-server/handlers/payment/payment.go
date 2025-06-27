package payment

import (
	"fmt"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/render"
	"log/slog"
	"net/http"
	"strconv"
	"wfsync/lib/api/response"
	"wfsync/lib/sl"
)

type Core interface {
	StripePaymentLink(amount int64) (string, error)
}

func Hold(log *slog.Logger, handler Core) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mod := sl.Module("http.handlers.payment")
		sum := chi.URLParam(r, "sum")

		logger := log.With(
			mod,
			slog.String("request_id", middleware.GetReqID(r.Context())),
			slog.String("sum", sum),
		)

		if handler == nil {
			logger.Error("stripe service not available")
			render.JSON(w, r, response.Error("Stripe service not available"))
			return
		}

		amount, err := strconv.ParseInt(sum, 10, 64)
		if err != nil {
			log.Warn("invalid amount")
			render.Status(r, 400)
			render.JSON(w, r, response.Error("Invalid amount"))
			return
		}

		link, err := handler.StripePaymentLink(amount)
		if err != nil {
			logger.Error("get payment link", sl.Err(err))
			render.Status(r, 400)
			render.JSON(w, r, response.Error(fmt.Sprintf("Get link: %v", err)))
			return
		}
		logger.Debug("payment link created")

		render.JSON(w, r, response.Ok(link))
	}
}
