package timeout

import (
	"context"
	"net/http"
	"time"
)

// Timeout middleware adds a timeout to the request context.
// `timeout` is the duration in seconds.
func Timeout(timeout time.Duration) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		fn := func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), timeout*time.Second)
			defer func() {
				cancel()
				//if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				//	w.WriteHeader(http.StatusGatewayTimeout)
				//}
			}()

			r = r.WithContext(ctx)
			next.ServeHTTP(w, r)
		}
		return http.HandlerFunc(fn)
	}
}
