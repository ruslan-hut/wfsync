package core

import (
	"context"
	"fmt"
	"github.com/stripe/stripe-go/v76"
	"log/slog"
	"time"
	"wfsync/entity"
	"wfsync/internal/stripeclient"
	"wfsync/lib/sl"
)

type Core struct {
	sc  *stripeclient.StripeClient
	log *slog.Logger
}

func New(sc *stripeclient.StripeClient, log *slog.Logger) Core {
	if sc == nil {
		panic("stripe client is nil")
	}
	return Core{
		sc:  sc,
		log: log.With(sl.Module("core")),
	}
}

func (c Core) AuthenticateByToken(token string) (*entity.User, error) {
	return nil, fmt.Errorf("not implemented")
}

func (c Core) StripeVerifySignature(payload []byte, header string, tolerance time.Duration) bool {
	return c.sc.VerifySignature(payload, header, tolerance)
}

func (c Core) StripeEvent(ctx context.Context, evt *stripe.Event) {
	c.sc.HandleEvent(ctx, evt)
}
