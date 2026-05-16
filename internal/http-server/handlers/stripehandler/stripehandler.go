package stripehandler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"
	"wfsync/lib/sl"

	"github.com/stripe/stripe-go/v76"
)

type Core interface {
	StripeVerifySignature(payload []byte, header string, tolerance time.Duration) bool
	StripeEvent(ctx context.Context, evt *stripe.Event)
}

func Event(logger *slog.Logger, handler Core) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		const tolerance = 5 * time.Minute
		log := logger.With(
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
		)

		// Stripe webhook payloads are small; cap at 256KB to prevent unbounded
		// memory use before signature verification.
		r.Body = http.MaxBytesReader(w, r.Body, 256*1024)
		payload, err := io.ReadAll(r.Body)
		if err != nil {
			log.With(
				sl.Err(err),
			).Error("read request body")
			http.Error(w, "read", http.StatusBadRequest)
			return
		}

		sig := r.Header.Get("Stripe-Signature")
		if !handler.StripeVerifySignature(payload, sig, tolerance) {
			log.Error("invalid webhook signature")
			http.Error(w, "signature", http.StatusBadRequest)
			return
		}

		var evt stripe.Event
		if err = json.Unmarshal(payload, &evt); err != nil {
			log.With(
				sl.Err(err),
			).Error("unmarshal event")
			http.Error(w, "json", http.StatusBadRequest)
			return
		}

		log = log.With(
			slog.String("event_id", evt.ID),
			slog.Any("type", evt.Type),
		)

		// Process asynchronously so we ACK within Stripe's 30s webhook timeout
		// regardless of how long downstream wFirma/OpenCart calls take. We use
		// a fresh background context since r.Context() is cancelled once we
		// return; failures inside StripeEvent are persisted via the retry queue.
		go handler.StripeEvent(context.Background(), &evt)

		w.WriteHeader(http.StatusOK)
	}
}
