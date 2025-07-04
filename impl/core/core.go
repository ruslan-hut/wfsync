package core

import (
	"context"
	"fmt"
	"github.com/stripe/stripe-go/v76"
	"io"
	"log/slog"
	"time"
	"wfsync/entity"
	"wfsync/internal/stripeclient"
	"wfsync/lib/sl"
	occlient "wfsync/opencart/oc-client"
)

type AuthService interface {
	UserByToken(token string) (*entity.User, error)
}

type InvoiceService interface {
	DownloadInvoice(ctx context.Context, invoiceID string) (io.ReadCloser, *entity.FileMeta, error)
	RegisterInvoice(ctx context.Context, params *entity.CheckoutParams) error
}

type Core struct {
	sc   *stripeclient.StripeClient
	oc   *occlient.Opencart
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

func (c *Core) SetInvoiceService(inv InvoiceService) {
	c.inv = inv
}

func (c *Core) SetAuthService(auth AuthService) {
	c.auth = auth
}

func (c *Core) SetOpencart(oc *occlient.Opencart) {
	c.oc = oc
}

func (c *Core) AuthenticateByToken(token string) (*entity.User, error) {
	if c.auth == nil {
		return nil, fmt.Errorf("auth service not connected")
	}
	return c.auth.UserByToken(token)
}

func (c *Core) StripeVerifySignature(payload []byte, header string, tolerance time.Duration) bool {
	return c.sc.VerifySignature(payload, header, tolerance)
}

func (c *Core) StripeEvent(ctx context.Context, evt *stripe.Event) {
	params := c.sc.HandleEvent(evt)
	if params == nil {
		return
	}
	// try to read invoice items from the site database
	if c.oc != nil && params.OrderId != "" {
		items, err := c.oc.OrderLines(params.OrderId)
		if err != nil {
			c.log.With(
				sl.Err(err),
			).Error("get order lines")
		}
		if items != nil && len(items) > 0 {
			params.LineItems = items
		}
	}
	err := c.inv.RegisterInvoice(ctx, params)
	if err != nil {
		c.log.With(
			sl.Err(err),
		).Error("register invoice")
	}
}

func (c *Core) WFirmaInvoiceDownload(ctx context.Context, invoiceID string) (io.ReadCloser, *entity.FileMeta, error) {
	if c.inv == nil {
		return nil, nil, fmt.Errorf("invoice service not connected")
	}
	return c.inv.DownloadInvoice(ctx, invoiceID)
}

func (c *Core) StripePaymentLink(params *entity.CheckoutParams) (*entity.Payment, error) {
	params.Source = entity.SourceApi
	return c.sc.HoldAmount(params)
}
