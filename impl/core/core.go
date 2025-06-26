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

type AuthService interface {
	UserByToken(token string) (*entity.User, error)
}

type InvoiceService interface {
	Download(ctx context.Context, invoiceID string) (string, error)
}

type Core struct {
	sc   *stripeclient.StripeClient
	inv  InvoiceService
	auth AuthService
	log  *slog.Logger
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

func (c Core) SetInvoiceService(inv InvoiceService) {
	c.inv = inv
}

func (c Core) SetAuthService(auth AuthService) {
	c.auth = auth
}

func (c Core) AuthenticateByToken(token string) (*entity.User, error) {
	if c.auth == nil {
		return nil, fmt.Errorf("auth service not connected")
	}
	return c.auth.UserByToken(token)
}

func (c Core) StripeVerifySignature(payload []byte, header string, tolerance time.Duration) bool {
	return c.sc.VerifySignature(payload, header, tolerance)
}

func (c Core) StripeEvent(ctx context.Context, evt *stripe.Event) {
	c.sc.HandleEvent(ctx, evt)
}

func (c Core) WFirmaInvoiceDownload(ctx context.Context, invoiceID string) (string, error) {
	if c.inv == nil {
		return "", fmt.Errorf("invoice service not connected")
	}
	return c.inv.Download(ctx, invoiceID)
}
