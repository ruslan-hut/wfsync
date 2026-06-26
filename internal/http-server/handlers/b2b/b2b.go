package b2b

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"wfsync/entity"
	"wfsync/lib/api/cont"
	"wfsync/lib/sl"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/render"
)

type Core interface {
	B2BCreateProforma(ctx context.Context, order *entity.B2BOrder) (*entity.Payment, error)
	B2BCreateInvoice(ctx context.Context, order *entity.B2BOrder) (*entity.Payment, error)
}

// urlResponse carries the URL of the first generated document plus the full list.
// URL stays for backward compatibility with clients that read a single field;
// URLs is the authoritative list and contains every part when the order was
// split across multiple wFirma invoices (and a single entry otherwise).
type urlResponse struct {
	URL  string   `json:"url"`
	URLs []string `json:"urls"`
}

// buildURLResponse extracts the URL list from a payment, including all split parts.
func buildURLResponse(payment *entity.Payment) urlResponse {
	urls := []string{payment.Link}
	if len(payment.Parts) > 1 {
		urls = urls[:0]
		for _, part := range payment.Parts {
			if part == nil || part.Link == "" {
				continue
			}
			urls = append(urls, part.Link)
		}
	}
	return urlResponse{URL: payment.Link, URLs: urls}
}

type errorResponse struct {
	Error string `json:"error"`
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
			render.JSON(w, r, errorResponse{Error: "User not found"})
			return
		}

		if !user.WFirmaAllowInvoice {
			log.Error("invoice not allowed")
			render.Status(r, 403)
			render.JSON(w, r, errorResponse{Error: "Invoice not allowed"})
			return
		}

		if handler == nil {
			log.Error("b2b service not available")
			render.Status(r, 500)
			render.JSON(w, r, errorResponse{Error: "B2B service not available"})
			return
		}

		var order entity.B2BOrder
		if err := render.Bind(r, &order); err != nil {
			log.Warn("invalid request body", sl.Err(err))
			render.Status(r, 400)
			render.JSON(w, r, errorResponse{Error: fmt.Sprintf("Invalid request: %v", err)})
			return
		}

		log = log.With(slog.String("order_number", order.OrderNumber))

		payment, err := handler.B2BCreateProforma(r.Context(), &order)
		if err != nil {
			if errors.Is(err, entity.ErrVATRateMismatch) {
				log.Warn("proforma vat rate mismatch", sl.Err(err))
				render.Status(r, 400)
				render.JSON(w, r, errorResponse{Error: err.Error()})
				return
			}
			log.Error("proforma creation", sl.Err(err))
			render.Status(r, 500)
			render.JSON(w, r, errorResponse{Error: fmt.Sprintf("Request failed: %v", err)})
			return
		}
		log.With(
			slog.String("proforma_id", payment.Id),
		).Debug("proforma created")

		render.JSON(w, r, buildURLResponse(payment))
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
			render.JSON(w, r, errorResponse{Error: "User not found"})
			return
		}

		if !user.WFirmaAllowInvoice {
			log.Error("invoice not allowed")
			render.Status(r, 403)
			render.JSON(w, r, errorResponse{Error: "Invoice not allowed"})
			return
		}

		if handler == nil {
			log.Error("b2b service not available")
			render.Status(r, 500)
			render.JSON(w, r, errorResponse{Error: "B2B service not available"})
			return
		}

		var order entity.B2BOrder
		if err := render.Bind(r, &order); err != nil {
			log.Warn("invalid request body", sl.Err(err))
			render.Status(r, 400)
			render.JSON(w, r, errorResponse{Error: fmt.Sprintf("Invalid request: %v", err)})
			return
		}

		log = log.With(slog.String("order_number", order.OrderNumber))

		payment, err := handler.B2BCreateInvoice(r.Context(), &order)
		if err != nil {
			if errors.Is(err, entity.ErrVATRateMismatch) {
				log.Warn("invoice vat rate mismatch", sl.Err(err))
				render.Status(r, 400)
				render.JSON(w, r, errorResponse{Error: err.Error()})
				return
			}
			log.Error("invoice creation", sl.Err(err))
			render.Status(r, 500)
			render.JSON(w, r, errorResponse{Error: fmt.Sprintf("Request failed: %v", err)})
			return
		}
		log.With(
			slog.String("invoice_id", payment.Id),
		).Debug("invoice created")

		render.JSON(w, r, buildURLResponse(payment))
	}
}
