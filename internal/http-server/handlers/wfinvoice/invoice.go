package wfinvoice

import (
	"context"
	"fmt"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/render"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"wfsync/entity"
	"wfsync/lib/api/response"
	"wfsync/lib/sl"
)

type Core interface {
	WFirmaInvoiceDownload(ctx context.Context, invID string) (io.ReadCloser, *entity.FileMeta, error)
}

func Download(logger *slog.Logger, handler Core) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mod := sl.Module("http.handlers.order")
		invoiceId := chi.URLParam(r, "id")

		log := logger.With(
			mod,
			slog.String("request_id", middleware.GetReqID(r.Context())),
			slog.String("invoice_id", invoiceId),
		)

		if handler == nil {
			log.Error("invoice service not available")
			render.JSON(w, r, response.Error("Invoice search not available"))
			return
		}

		_, err := strconv.ParseInt(invoiceId, 10, 64)
		if err != nil {
			log.Warn("invalid order id")
			render.Status(r, 400)
			render.JSON(w, r, response.Error("Invalid invoice id"))
			return
		}

		fileStream, meta, err := handler.WFirmaInvoiceDownload(context.Background(), invoiceId)
		if err != nil {
			log.Error("invoice download", sl.Err(err))
			render.JSON(w, r, response.Error(fmt.Sprintf("Request failed: %v", err)))
			return
		}
		defer fileStream.Close()

		w.Header().Set("Content-Type", meta.ContentType)
		if meta.ContentLength >= 0 {
			w.Header().Set("Content-Length", strconv.FormatInt(meta.ContentLength, 10))
		}

		if _, err = io.Copy(w, fileStream); err != nil {
			log.Error("failed to copy file", sl.Err(err))
		}
	}
}
