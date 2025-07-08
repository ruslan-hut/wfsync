package core

import (
	"context"
	"fmt"
	"github.com/stripe/stripe-go/v76"
	"io"
	"log/slog"
	"time"
	"wfsync/entity"
	"wfsync/internal/config"
	"wfsync/internal/stripeclient"
	"wfsync/lib/sl"
	occlient "wfsync/opencart/oc-client"
)

type AuthService interface {
	UserByToken(token string) (*entity.User, error)
}

type InvoiceService interface {
	DownloadInvoice(ctx context.Context, invoiceID string) (io.ReadCloser, *entity.FileMeta, error)
	RegisterInvoice(ctx context.Context, params *entity.CheckoutParams) (*entity.Payment, error)
	RegisterProforma(ctx context.Context, params *entity.CheckoutParams) (*entity.Payment, error)
}

type Core struct {
	sc       *stripeclient.StripeClient
	oc       *occlient.Opencart
	inv      InvoiceService
	auth     AuthService
	filePath string
	log      *slog.Logger
}

func New(conf *config.Config, log *slog.Logger) Core {
	return Core{
		filePath: conf.FilePath,
		log:      log.With(sl.Module("core")),
	}
}

func (c *Core) SetStripeClient(sc *stripeclient.StripeClient) {
	c.sc = sc
}

func (c *Core) SetInvoiceService(inv InvoiceService) {
	c.inv = inv
}

func (c *Core) SetAuthService(auth AuthService) {
	c.auth = auth
}

func (c *Core) SetOpencart(oc *occlient.Opencart) {
	if oc == nil {
		c.log.Warn("opencart client is nil, some features may not work")
		return
	}
	c.oc = oc.WithUrlHandler(c.StripePayAmount)
	c.oc = oc.WithProformaHandler(c.WFirmaRegisterProforma)
	c.oc.Start()
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
	_, err := c.inv.RegisterInvoice(ctx, params)
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

func (c *Core) WFirmaRegisterProforma(params *entity.CheckoutParams) (*entity.Payment, error) {
	if c.inv == nil {
		return nil, fmt.Errorf("invoice service not connected")
	}
	return c.inv.RegisterProforma(context.Background(), params)
}

func (c *Core) StripeHoldAmount(params *entity.CheckoutParams) (*entity.Payment, error) {
	err := params.Validate()
	if err != nil {
		return nil, err
	}
	return c.sc.HoldAmount(params)
}

func (c *Core) StripePayAmount(params *entity.CheckoutParams) (*entity.Payment, error) {
	err := params.Validate()
	if err != nil {
		return nil, err
	}
	return c.sc.PayAmount(params)
}
