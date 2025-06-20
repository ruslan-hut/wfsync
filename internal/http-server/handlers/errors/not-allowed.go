package errors

import (
	"github.com/go-chi/render"
	"log/slog"
	"net/http"
	"wfsync/lib/api/response"
)

func NotAllowed(_ *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		//mod := sl.Module("http.handlers.errors")

		render.Status(r, 405)
		render.JSON(w, r, response.Error("Method not allowed"))
	}
}
