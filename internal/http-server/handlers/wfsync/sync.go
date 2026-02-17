package wfsync

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"wfsync/entity"
	"wfsync/lib/api/cont"
	"wfsync/lib/api/response"
	"wfsync/lib/sl"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/render"
)

// Core defines the methods required by the sync handlers.
type Core interface {
	WFirmaSyncFromRemote(ctx context.Context, from, to string) (*entity.SyncResult, error)
	WFirmaSyncToRemote(ctx context.Context, from, to string) (*entity.SyncResult, error)
}

var datePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// SyncFromRemote handles POST /v1/wf/sync/pull — pulls invoices from wFirma to local DB.
func SyncFromRemote(logger *slog.Logger, handler Core) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mod := sl.Module("http.handlers.wfsync")
		user := cont.GetUser(r.Context())

		log := logger.With(
			mod,
			slog.String("request_id", middleware.GetReqID(r.Context())),
			slog.String("user", user.Username),
		)

		if !user.WFirmaAllowInvoice {
			log.Warn("invoice sync not allowed")
			render.Status(r, 403)
			render.JSON(w, r, response.Error("Invoice sync not allowed"))
			return
		}

		from := r.URL.Query().Get("from")
		to := r.URL.Query().Get("to")
		if !datePattern.MatchString(from) || !datePattern.MatchString(to) {
			render.Status(r, 400)
			render.JSON(w, r, response.Error("Invalid date format, expected YYYY-MM-DD"))
			return
		}

		result, err := handler.WFirmaSyncFromRemote(r.Context(), from, to)
		if err != nil {
			log.Error("sync from remote", sl.Err(err))
			render.JSON(w, r, response.Error(fmt.Sprintf("Sync failed: %v", err)))
			return
		}

		render.JSON(w, r, response.Ok(result))
	}
}

// SyncToRemote handles POST /v1/wf/sync/push — pushes local invoices to wFirma.
func SyncToRemote(logger *slog.Logger, handler Core) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mod := sl.Module("http.handlers.wfsync")
		user := cont.GetUser(r.Context())

		log := logger.With(
			mod,
			slog.String("request_id", middleware.GetReqID(r.Context())),
			slog.String("user", user.Username),
		)

		if !user.WFirmaAllowInvoice {
			log.Warn("invoice sync not allowed")
			render.Status(r, 403)
			render.JSON(w, r, response.Error("Invoice sync not allowed"))
			return
		}

		from := r.URL.Query().Get("from")
		to := r.URL.Query().Get("to")
		if !datePattern.MatchString(from) || !datePattern.MatchString(to) {
			render.Status(r, 400)
			render.JSON(w, r, response.Error("Invalid date format, expected YYYY-MM-DD"))
			return
		}

		result, err := handler.WFirmaSyncToRemote(r.Context(), from, to)
		if err != nil {
			log.Error("sync to remote", sl.Err(err))
			render.JSON(w, r, response.Error(fmt.Sprintf("Sync failed: %v", err)))
			return
		}

		render.JSON(w, r, response.Ok(result))
	}
}
