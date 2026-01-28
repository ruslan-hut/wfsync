package b2b

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"wfsync/entity"
	"wfsync/lib/api/cont"
	"wfsync/lib/api/response"
	"wfsync/lib/sl"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/render"
)

type Core interface {
	B2BCreateProforma(ctx context.Context, order *entity.B2BOrder) (*entity.Payment, error)
	B2BCreateInvoice(ctx context.Context, order *entity.B2BOrder) (*entity.Payment, error)
}

func CreateProforma(logger *slog.Logger, handler Core) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mod := sl.Module("http.handlers.b2b")

		log := logger.With(
			mod,
			slog.String("request_id", middleware.GetReqID(r.Context())),
		)

		user := cont.GetUser(r.Context())
		if user == nil {
			log.Error("user not found")
			render.Status(r, 401)
			render.JSON(w, r, response.Error("User not found"))
			return
		}

		if !user.WFirmaAllowInvoice {
			log.Error("invoice not allowed")
			render.Status(r, 403)
			render.JSON(w, r, response.Error("Invoice not allowed"))
			return
		}

		if handler == nil {
			log.Error("b2b service not available")
			render.JSON(w, r, response.Error("B2B service not available"))
			return
		}

		var order entity.B2BOrder
		if err := render.Bind(r, &order); err != nil {
			log.Warn("invalid request body", sl.Err(err))
			render.Status(r, 400)
			render.JSON(w, r, response.Error(fmt.Sprintf("Invalid request: %v", err)))
			return
		}

		log = log.With(slog.String("order_number", order.OrderNumber))

		payment, err := handler.B2BCreateProforma(r.Context(), &order)
		if err != nil {
			log.Error("proforma creation", sl.Err(err))
			render.JSON(w, r, response.Error(fmt.Sprintf("Request failed: %v", err)))
			return
		}
		log.With(
			slog.String("proforma_id", payment.Id),
		).Debug("proforma created")

		render.JSON(w, r, response.Ok(payment))
	}
}

func CreateInvoice(logger *slog.Logger, handler Core) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mod := sl.Module("http.handlers.b2b")

		log := logger.With(
			mod,
			slog.String("request_id", middleware.GetReqID(r.Context())),
		)

		user := cont.GetUser(r.Context())
		if user == nil {
			log.Error("user not found")
			render.Status(r, 401)
			render.JSON(w, r, response.Error("User not found"))
			return
		}

		if !user.WFirmaAllowInvoice {
			log.Error("invoice not allowed")
			render.Status(r, 403)
			render.JSON(w, r, response.Error("Invoice not allowed"))
			return
		}

		if handler == nil {
			log.Error("b2b service not available")
			render.JSON(w, r, response.Error("B2B service not available"))
			return
		}

		var order entity.B2BOrder
		if err := render.Bind(r, &order); err != nil {
			log.Warn("invalid request body", sl.Err(err))
			render.Status(r, 400)
			render.JSON(w, r, response.Error(fmt.Sprintf("Invalid request: %v", err)))
			return
		}

		log = log.With(slog.String("order_number", order.OrderNumber))

		payment, err := handler.B2BCreateInvoice(r.Context(), &order)
		if err != nil {
			log.Error("invoice creation", sl.Err(err))
			render.JSON(w, r, response.Error(fmt.Sprintf("Request failed: %v", err)))
			return
		}
		log.With(
			slog.String("invoice_id", payment.Id),
		).Debug("invoice created")

		render.JSON(w, r, response.Ok(payment))
	}
}
