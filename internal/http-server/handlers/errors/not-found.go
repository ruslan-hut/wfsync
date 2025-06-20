package errors

import (
	"github.com/go-chi/render"
	"log/slog"
	"net/http"
	"wfsync/lib/api/response"
)

func NotFound(_ *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		//mod := sl.Module("http.handlers.errors")

		render.Status(r, 404)
		render.JSON(w, r, response.Error("Requested resource not found"))
	}
}
