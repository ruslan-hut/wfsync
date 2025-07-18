package core

import (
	"context"
	"fmt"
	"github.com/stripe/stripe-go/v76"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
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
	DownloadInvoice(ctx context.Context, invoiceID string) (string, *entity.FileMeta, error)
	RegisterInvoice(ctx context.Context, params *entity.CheckoutParams) (*entity.Payment, error)
	RegisterProforma(ctx context.Context, params *entity.CheckoutParams) (*entity.Payment, error)
}

type Core struct {
	sc       *stripeclient.StripeClient
	oc       *occlient.Opencart
	inv      InvoiceService
	auth     AuthService
	filePath string
	fileUrl  string
	log      *slog.Logger
}

func New(conf *config.Config, log *slog.Logger) Core {
	return Core{
		filePath: conf.FilePath,
		fileUrl:  conf.OpenCart.FileUrl,
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
	fileName, meta, err := c.inv.DownloadInvoice(ctx, invoiceID)
	if err != nil {
		return nil, nil, err
	}
	filePath := filepath.Join(c.filePath, fileName)
	file, err := os.Open(filePath)
	if err != nil {
		return nil, nil, fmt.Errorf("open file: %w", err)
	}
	return file, meta, nil
}

func (c *Core) WFirmaRegisterProforma(params *entity.CheckoutParams) (*entity.Payment, error) {
	if c.inv == nil {
		return nil, fmt.Errorf("invoice service not connected")
	}

	ctx := context.Background()
	var payment *entity.Payment
	var err error

	// when the invoice was already registered, we will get InvoiceId in CheckoutParams
	// in this case we need only to download a file

	if params.InvoiceId == "" {
		payment, err = c.inv.RegisterProforma(ctx, params)
		if err != nil {
			return nil, err
		}
	} else {
		payment = &entity.Payment{
			Id:      params.InvoiceId,
			Amount:  params.Total,
			OrderId: params.OrderId,
		}
	}

	fileName, _, err := c.inv.DownloadInvoice(ctx, payment.Id)
	if err != nil {
		return nil, fmt.Errorf("download invoice: %w", err)
	}
	link, err := url.JoinPath(c.fileUrl, fileName)
	if err != nil {
		return nil, fmt.Errorf("join url: %w", err)
	}

	payment.Link = link
	payment.InvoiceFile = fileName

	return payment, nil
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
	err = params.ValidateTotal()
	if err != nil {
		// not an error because may have a difference in 0.01 cent
		c.log.With(
			slog.String("order_id", params.OrderId),
			sl.Err(err),
		).Warn("invalid order total")
		err = params.RefineTotal(0)
		if err != nil {
			return nil, err
		}
	}
	return c.sc.PayAmount(params)
}
