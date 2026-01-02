package timeout

import (
	"context"
	"net/http"
	"time"
)

// Timeout middleware adds a timeout to the request context.
// The timeout parameter is in seconds.
// Handlers should check ctx.Err() to detect deadline exceeded.
func Timeout(timeout time.Duration) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		fn := func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			defer cancel()

			r = r.WithContext(ctx)
			next.ServeHTTP(w, r)
		}
		return http.HandlerFunc(fn)
	}
}
