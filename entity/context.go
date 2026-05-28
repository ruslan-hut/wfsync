package entity

import "context"

// retryContextKey marks a context as belonging to a background retry attempt.
type retryContextKey struct{}

// WithRetry returns a context flagged as a retry attempt. The invoice layer uses
// this to keep retry failures local (no per-attempt Telegram alert or full-response
// dump), since the original failure was already reported when the job was enqueued.
func WithRetry(ctx context.Context) context.Context {
	return context.WithValue(ctx, retryContextKey{}, true)
}

// IsRetry reports whether ctx was flagged by WithRetry.
func IsRetry(ctx context.Context) bool {
	v, _ := ctx.Value(retryContextKey{}).(bool)
	return v
}
