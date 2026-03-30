package core

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
	"wfsync/entity"
	"wfsync/internal/config"
	"wfsync/internal/stripeclient"
	"wfsync/internal/wfirma"
	"wfsync/lib/sl"
	occlient "wfsync/opencart/oc-client"

	"github.com/stripe/stripe-go/v76"
)

const (
	// OrderStatusHoldConfirmed is the OpenCart order status set when a Stripe hold is successfully confirmed.
	OrderStatusHoldConfirmed = 17
)

type AuthService interface {
	UserByToken(token string) (*entity.User, error)
}

type InvoiceService interface {
	DownloadInvoice(ctx context.Context, invoiceID string) (string, *entity.FileMeta, error)
	RegisterInvoice(ctx context.Context, params *entity.CheckoutParams) (*entity.Payment, error)
	RegisterProforma(ctx context.Context, params *entity.CheckoutParams) (*entity.Payment, error)
	SyncFromRemote(ctx context.Context, from, to string) (*entity.SyncResult, error)
	SyncToRemote(ctx context.Context, from, to string) (*entity.SyncResult, error)
	FindInvoices(ctx context.Context, from, to string) ([]*entity.LocalInvoice, error)
}

// PaymentDatabase provides access to payment-related data in MongoDB.
type PaymentDatabase interface {
	GetStripeOrderIds(orderIds []string) (map[string]bool, error)
}

type Core struct {
	sc         *stripeclient.StripeClient
	oc         *occlient.Opencart
	inv        InvoiceService
	db         PaymentDatabase
	auth       AuthService
	retryQueue *RetryQueue
	filePath   string
	fileUrl    string
	log        *slog.Logger
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

func (c *Core) SetPaymentDatabase(db PaymentDatabase) {
	c.db = db
}

func (c *Core) SetRetryQueue(rq *RetryQueue) {
	c.retryQueue = rq
}

func (c *Core) SetOpencart(oc *occlient.Opencart) {
	if oc == nil {
		c.log.Warn("opencart client is nil, some features may not work")
		return
	}
	c.oc = oc.WithUrlHandler(c.StripePayAmount)
	c.oc = oc.WithProformaHandler(c.WFirmaRegisterProforma)
	c.oc = oc.WithInvoiceHandler(c.WFirmaRegisterInvoice)
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
	// create checkout params from the stripe event
	params := c.sc.HandleEvent(evt)
	if params == nil {
		return
	}

	// save payment data to OpenCart regardless of paid status
	if c.oc != nil && params.OrderId != "" {
		status := params.Status
		if status == "" {
			status = "pending"
		}
		if params.Paid {
			status = "paid"
		}
		if err := c.oc.SavePaymentData(params.OrderId, params.PaymentId, params.SessionId, status, params.Total); err != nil {
			c.log.With(
				sl.Err(err),
				slog.String("order_id", params.OrderId),
			).Error("save payment data")
		}

		// Update OpenCart order status when hold is confirmed
		if params.Status == string(stripe.PaymentIntentStatusRequiresCapture) {
			comment := fmt.Sprintf("Hold confirmed: %d %s (pi: %s)",
				params.Total, params.Currency, params.PaymentId)
			if err := c.oc.ChangeOrderStatus(params.OrderId, OrderStatusHoldConfirmed, comment); err != nil {
				c.log.With(
					sl.Err(err),
					slog.String("order_id", params.OrderId),
				).Error("change order status")
			}
		}
	}

	if !params.Paid {
		return
	}

	// try to read invoice items from the site database
	if c.oc != nil && params.OrderId != "" {
		orderId, err := strconv.ParseInt(params.OrderId, 10, 64)
		if err != nil {
			c.log.With(
				slog.String("order_id", params.OrderId),
				slog.String("session_id", params.SessionId),
				slog.String("event_id", evt.ID),
				slog.String("email", params.ClientDetails.Email),
				slog.Int64("total", params.Total),
				slog.String("currency", params.Currency),
				slog.String("tg_topic", entity.TopicError),
			).Warn("no opencart order id in stripe session, skipping invoice creation")
			return
		}
		order, err := c.oc.GetOrder(orderId)
		if err != nil {
			c.log.With(
				sl.Err(err),
				slog.Int64("order_id", orderId),
			).Error("get order")
		}
		if order == nil || len(order.LineItems) == 0 {
			c.log.With(
				slog.Int64("order_id", orderId),
				slog.String("session_id", params.SessionId),
				slog.String("event_id", evt.ID),
				slog.String("email", params.ClientDetails.Email),
				slog.Int64("total", params.Total),
				slog.String("currency", params.Currency),
				slog.String("tg_topic", entity.TopicError),
			).Warn("opencart order not found or has no items, skipping invoice creation")
			return
		}
		// Replace Stripe totals with OpenCart values so that TaxRate() uses consistent data.
		// The site already applies the correct VAT rate per destination country (OSS scheme),
		// and wfirma accepts those rates as-is (e.g. 21% for NL, 19% for DE).
		params.LineItems = order.LineItems
		params.Total = order.Total
		params.Shipping = order.Shipping
		params.TaxValue = order.TaxValue
		params.TaxTitle = order.TaxTitle
		params.SubTotal = order.SubTotal
		params.CustomerGroup = order.CustomerGroup
	}

	if params.InvoiceId != "" && params.OrderId != "" {
		c.log.With(
			slog.String("invoice_id", params.InvoiceId),
			slog.String("order_id", params.OrderId),
			slog.String("event_id", evt.ID),
		).Warn("invoice already registered")
		return
	}

	// register new invoice
	payment, err := c.inv.RegisterInvoice(ctx, params)
	if err != nil {
		c.log.With(
			sl.Err(err),
			slog.String("event_id", evt.ID),
			slog.String("order_id", params.OrderId),
		).Error("register invoice")
		if c.retryQueue != nil {
			c.retryQueue.Enqueue(params, err.Error())
		}
		return
	}
	// save invoice id to a site database
	if payment != nil && c.oc != nil {
		err = c.oc.SaveInvoiceId(params.OrderId, payment.Id, payment.InvoiceFile)
		if err != nil {
			c.log.With(
				sl.Err(err),
			).Error("save invoice id")
		}
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

func (c *Core) WFirmaOrderToInvoice(ctx context.Context, orderId int64, useCurrentDate bool) (*entity.CheckoutParams, error) {
	if c.inv == nil {
		return nil, fmt.Errorf("invoice service not connected")
	}
	if c.oc == nil {
		return nil, fmt.Errorf("opencart service not connected")
	}

	params, err := c.oc.GetOrder(orderId)
	if err != nil {
		return nil, err
	}
	if params == nil {
		return nil, fmt.Errorf("order not found")
	}

	if useCurrentDate {
		params.Created = time.Now()
	}

	log := c.log.With(
		slog.String("order_id", params.OrderId),
		slog.Int64("total", params.Total),
		slog.String("date", params.Created.Format("2006-01-02")),
	)

	linesTotal := params.ItemsTotal()
	if linesTotal != params.Total {
		log.With(
			slog.Int64("lines_total", linesTotal),
			slog.Int64("diff", params.Total-linesTotal),
		).Warn("order total mismatch")
		//params.RecalcWithDiscount()
	}

	params.Paid = false

	log.Debug("order to invoice")

	payment, err := c.inv.RegisterInvoice(ctx, params)
	if err != nil {
		return nil, err
	}
	params.InvoiceId = payment.Id

	err = c.oc.SaveInvoiceId(params.OrderId, payment.Id, payment.InvoiceFile)
	if err != nil {
		log.Warn("save invoice id", sl.Err(err))
	}

	return params, nil
}

func (c *Core) WFirmaRegisterProforma(ctx context.Context, params *entity.CheckoutParams) (*entity.Payment, error) {
	if c.inv == nil {
		return nil, fmt.Errorf("invoice service not connected")
	}

	var payment *entity.Payment
	var err error

	// when the invoice was already registered, we will get ProformaId and ProformaFile in CheckoutParams
	// in this case we need to delete the old file before downloading a new one

	if params.ProformaFile != "" {
		fileName := filepath.Join(c.filePath, params.ProformaFile)
		if _, err = os.Stat(fileName); err == nil {
			err = os.Remove(fileName)
			if err != nil {
				c.log.With(
					slog.String("order_id", params.OrderId),
					slog.String("path", c.filePath),
					slog.String("file_id", params.ProformaId),
					slog.String("file_name", params.ProformaFile),
					sl.Err(err),
				).Warn("remove file")
			}
		}
		params.ProformaFile = ""
		params.ProformaId = ""
	}

	payment, err = c.inv.RegisterProforma(ctx, params)
	if err != nil {
		return nil, err
	}

	fileName, link, err := c.downloadInvoice(ctx, params.ProformaFile, payment.Id)
	if err != nil {
		return nil, err
	}
	payment.Link = link
	payment.InvoiceFile = fileName

	return payment, nil
}

func (c *Core) WFirmaRegisterInvoice(ctx context.Context, params *entity.CheckoutParams) (*entity.Payment, error) {
	if c.inv == nil {
		return nil, fmt.Errorf("invoice service not connected")
	}

	var payment *entity.Payment
	var err error

	// when the invoice was already registered, we will get InvoiceId in CheckoutParams
	// in this case we need only to download a file

	if params.InvoiceId == "" {
		payment, err = c.inv.RegisterInvoice(ctx, params)
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

	fileName, link, err := c.downloadInvoice(ctx, params.InvoiceFile, payment.Id)
	if err != nil {
		return nil, err
	}
	payment.Link = link
	payment.InvoiceFile = fileName

	return payment, nil
}

func (c *Core) downloadInvoice(ctx context.Context, fileName, paymentId string) (string, string, error) {
	var err error
	if fileName == "" {
		fileName, _, err = c.inv.DownloadInvoice(ctx, paymentId)
		if err != nil {
			return "", "", fmt.Errorf("download invoice: %w", err)
		}
	}

	link, err := url.JoinPath(c.fileUrl, fileName)
	if err != nil {
		return "", "", fmt.Errorf("join url: %w", err)
	}
	return fileName, link, nil
}

func (c *Core) StripeHoldAmount(params *entity.CheckoutParams) (*entity.Payment, error) {
	err := params.Validate()
	if err != nil {
		return nil, err
	}
	return c.sc.HoldAmount(params)
}

func (c *Core) StripeCaptureAmount(sessionId string, amount int64) (*entity.Payment, error) {
	pm, err := c.sc.CaptureAmount(sessionId, amount)
	if err != nil {
		return nil, err
	}
	if c.oc != nil && pm.OrderId != "" {
		if saveErr := c.oc.SavePaymentData(pm.OrderId, pm.Id, sessionId, "paid", pm.Amount); saveErr != nil {
			c.log.With(sl.Err(saveErr), slog.String("order_id", pm.OrderId)).Error("update payment status after capture")
		}
	}
	return pm, nil
}

func (c *Core) StripeCancelPayment(sessionId, reason string) (*entity.Payment, error) {
	pm, err := c.sc.CancelPayment(sessionId, reason)
	if err != nil {
		return nil, err
	}
	if c.oc != nil && pm.OrderId != "" {
		if saveErr := c.oc.SavePaymentData(pm.OrderId, pm.Id, sessionId, "canceled", pm.Amount); saveErr != nil {
			c.log.With(sl.Err(saveErr), slog.String("order_id", pm.OrderId)).Error("update payment status after cancel")
		}
	}
	return pm, nil
}

func (c *Core) StripePayAmount(_ context.Context, params *entity.CheckoutParams) (*entity.Payment, error) {
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
		params.RecalcWithDiscount()
	}
	return c.sc.PayAmount(params)
}

func (c *Core) WFirmaOrderFileProforma(ctx context.Context, orderId int64) (*entity.Payment, error) {
	if c.inv == nil {
		return nil, fmt.Errorf("invoice service not connected")
	}
	if c.oc == nil {
		return nil, fmt.Errorf("opencart service not connected")
	}

	params, err := c.oc.GetOrder(orderId)
	if err != nil {
		return nil, err
	}
	if params == nil {
		return nil, fmt.Errorf("order not found")
	}

	payment, err := c.WFirmaRegisterProforma(ctx, params)
	if err != nil {
		return nil, err
	}
	if c.oc != nil {
		_ = c.oc.UpdateOrderWithProforma(orderId, payment.Id, payment.InvoiceFile)
	}
	return payment, nil
}

func (c *Core) WFirmaOrderFileInvoice(ctx context.Context, orderId int64) (*entity.Payment, error) {
	if c.inv == nil {
		return nil, fmt.Errorf("invoice service not connected")
	}
	if c.oc == nil {
		return nil, fmt.Errorf("opencart service not connected")
	}

	params, err := c.oc.GetOrder(orderId)
	if err != nil {
		return nil, err
	}
	if params == nil {
		return nil, fmt.Errorf("order not found")
	}

	payment, err := c.WFirmaRegisterInvoice(ctx, params)
	if err != nil {
		return nil, err
	}
	if c.oc != nil {
		_ = c.oc.UpdateOrderWithInvoice(orderId, payment.Id, payment.InvoiceFile)
	}
	return payment, nil
}

func (c *Core) WFirmaCreateProforma(ctx context.Context, params *entity.CheckoutParams) (*entity.Payment, error) {
	return c.WFirmaRegisterProforma(ctx, params)
}

func (c *Core) WFirmaCreateInvoice(ctx context.Context, params *entity.CheckoutParams) (*entity.Payment, error) {
	return c.WFirmaRegisterInvoice(ctx, params)
}

func (c *Core) B2BCreateProforma(ctx context.Context, order *entity.B2BOrder) (*entity.Payment, error) {
	params := order.ToCheckoutParams()
	return c.WFirmaRegisterProforma(ctx, params)
}

func (c *Core) B2BCreateInvoice(ctx context.Context, order *entity.B2BOrder) (*entity.Payment, error) {
	params := order.ToCheckoutParams()
	return c.WFirmaRegisterInvoice(ctx, params)
}

// InvoiceList returns a merged invoice list from WFirma, OpenCart, and MongoDB.
// WFirma is the preferred source for contractor name and date when both sources have data.
func (c *Core) InvoiceList(ctx context.Context, from, to string) ([]*entity.InvoiceListItem, error) {
	if c.inv == nil {
		return nil, fmt.Errorf("invoice service not connected")
	}

	// Step 1: fetch WFirma invoices
	wfInvoices, err := c.inv.FindInvoices(ctx, from, to)
	if err != nil {
		c.log.With(sl.Err(err)).Warn("find wfirma invoices for list")
		wfInvoices = nil // continue without wfirma data
	}

	// Index WFirma invoices by IdExternal (order_id)
	wfByOrder := make(map[string]*entity.LocalInvoice, len(wfInvoices))
	wfMatched := make(map[string]bool)
	for _, inv := range wfInvoices {
		if inv.IdExternal != "" {
			wfByOrder[inv.IdExternal] = inv
		}
	}

	// Step 2: fetch OpenCart orders
	var ocOrders []*entity.OrderSummary
	if c.oc != nil {
		ocOrders, err = c.oc.GetOrdersByDateRange(from, to)
		if err != nil {
			c.log.With(sl.Err(err)).Warn("get opencart orders for list")
			ocOrders = nil
		}
	}

	// Collect all order IDs for MongoDB lookup
	orderIds := make([]string, 0, len(ocOrders)+len(wfInvoices))
	for _, o := range ocOrders {
		orderIds = append(orderIds, o.OrderId)
	}
	for _, inv := range wfInvoices {
		if inv.IdExternal != "" {
			orderIds = append(orderIds, inv.IdExternal)
		}
	}

	// Step 3: fetch Stripe info from MongoDB
	var stripeOrders map[string]bool
	if c.db != nil && len(orderIds) > 0 {
		stripeOrders, err = c.db.GetStripeOrderIds(orderIds)
		if err != nil {
			c.log.With(sl.Err(err)).Warn("get stripe order ids for list")
		}
	}

	// Step 4: merge
	var items []*entity.InvoiceListItem

	// Process OpenCart orders first
	for _, oc := range ocOrders {
		item := &entity.InvoiceListItem{
			Date:           oc.DateAdded.Format("2006-01-02"),
			OrderStatus:    oc.OrderStatus,
			OrderId:        oc.OrderId,
			ContractorName: oc.ClientName,
			IsB2B:          wfirma.IsB2BCustomerGroup(oc.CustomerGroup),
			IsStripe:       stripeOrders[oc.OrderId],
			Currency:       oc.Currency,
		}
		if oc.Currency == "PLN" {
			item.TotalPLN = oc.Total
		} else if oc.Currency == "EUR" {
			item.TotalEUR = oc.Total
		}

		// If WFirma invoice exists for this order, prefer WFirma data
		if wf, ok := wfByOrder[oc.OrderId]; ok {
			wfMatched[oc.OrderId] = true
			item.InvoiceNumber = wf.Number
			item.Date = wf.Date // prefer WFirma date
			if wf.Contractor != nil && wf.Contractor.Name != "" {
				item.ContractorName = wf.Contractor.Name // prefer WFirma contractor
			}
		} else if oc.InvoiceId != "" {
			// OpenCart has wf_invoice but not found in WFirma date range
			item.InvoiceNumber = oc.InvoiceId
		}

		items = append(items, item)
	}

	// Add WFirma-only invoices (not matched by OpenCart)
	for _, inv := range wfInvoices {
		if inv.IdExternal != "" && wfMatched[inv.IdExternal] {
			continue
		}
		item := &entity.InvoiceListItem{
			Date:          inv.Date,
			OrderId:       inv.IdExternal,
			InvoiceNumber: inv.Number,
			IsStripe:      stripeOrders[inv.IdExternal],
			Currency:      inv.Currency,
		}
		if inv.Contractor != nil {
			item.ContractorName = inv.Contractor.Name
		}
		total := int64(inv.Total * 100)
		if inv.Currency == "PLN" {
			item.TotalPLN = total
		} else if inv.Currency == "EUR" {
			item.TotalEUR = total
		}
		items = append(items, item)
	}

	// Sort by date ascending
	sort.Slice(items, func(i, j int) bool {
		return items[i].Date < items[j].Date
	})

	return items, nil
}

func (c *Core) WFirmaSyncFromRemote(ctx context.Context, from, to string) (*entity.SyncResult, error) {
	if c.inv == nil {
		return nil, fmt.Errorf("invoice service not connected")
	}
	return c.inv.SyncFromRemote(ctx, from, to)
}

func (c *Core) WFirmaSyncToRemote(ctx context.Context, from, to string) (*entity.SyncResult, error) {
	if c.inv == nil {
		return nil, fmt.Errorf("invoice service not connected")
	}
	return c.inv.SyncToRemote(ctx, from, to)
}
