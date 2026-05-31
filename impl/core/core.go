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
	DeleteProforma(ctx context.Context, invoiceID string) error
	SyncFromRemote(ctx context.Context, from, to string) (*entity.SyncResult, error)
	SyncToRemote(ctx context.Context, from, to string) (*entity.SyncResult, error)
	FindInvoices(ctx context.Context, from, to string) ([]*entity.LocalInvoice, error)
	InvoiceExists(ctx context.Context, invoiceID string) (bool, error)
}

// PaymentDatabase provides access to payment-related data in MongoDB.
type PaymentDatabase interface {
	GetStripeOrderIds(orderIds []string) (map[string]bool, error)
	GetUnresolvedHeldParams(limit int) ([]*entity.CheckoutParams, error)
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
	if c.oc != nil {
		c.log.Warn("opencart already set; ignoring second SetOpencart to avoid goroutine leak")
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

	c.processInvoice(ctx, params)
}

// processInvoice enriches the order with authoritative OpenCart data, registers a
// wFirma invoice, persists the resulting invoice id back to OpenCart, and falls back
// to the retry queue on failure. It is shared by the Stripe webhook (paid event), the
// manual capture flow, and the payment reconciler. params.EventId is used for log
// correlation and as the retry-queue key. Returns the created payment, or nil when the
// invoice was skipped (no order / already registered) or registration failed and was
// handed off to the retry queue.
func (c *Core) processInvoice(ctx context.Context, params *entity.CheckoutParams) *entity.Payment {
	// try to read invoice items from the site database
	if c.oc != nil && params.OrderId != "" {
		orderId, err := c.oc.ResolveOrderId(params.OrderId)
		if err != nil {
			c.log.With(
				sl.Err(err),
				slog.String("order_id", params.OrderId),
				slog.String("session_id", params.SessionId),
			).Error("resolve opencart order id")
			return nil
		}
		if orderId == 0 {
			c.log.With(
				slog.String("order_id", params.OrderId),
				slog.String("session_id", params.SessionId),
				slog.String("event_id", params.EventId),
				slog.String("email", params.ClientDetails.Email),
				slog.Int64("total", params.Total),
				slog.String("currency", params.Currency),
				slog.String("tg_topic", entity.TopicError),
			).Warn("no opencart order id in stripe session, skipping invoice creation")
			return nil
		}
		// Normalize so the invoice and OpenCart writes target the numeric order id even
		// when the session carried a CRM ("ORD-<zoho>") id.
		params.OrderId = strconv.FormatInt(orderId, 10)
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
				slog.String("event_id", params.EventId),
				slog.String("email", params.ClientDetails.Email),
				slog.Int64("total", params.Total),
				slog.String("currency", params.Currency),
				slog.String("tg_topic", entity.TopicError),
			).Warn("opencart order not found or has no items, skipping invoice creation")
			return nil
		}
		// Order-level idempotency: a capture can now be observed by several independent
		// triggers (capture API, payment_intent.succeeded webhook, reconciler). If the
		// order already carries an invoice, stop here so they don't each register one.
		if order.InvoiceId != "" {
			c.log.With(
				slog.Int64("order_id", orderId),
				slog.String("invoice_id", order.InvoiceId),
				slog.String("event_id", params.EventId),
			).Debug("order already invoiced, skipping invoice creation")
			return nil
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
			slog.String("event_id", params.EventId),
		).Warn("invoice already registered")
		return nil
	}

	// register new invoice
	payment, err := c.inv.RegisterInvoice(ctx, params)
	if err != nil {
		// wfirma layer already reports the user-facing error to Telegram;
		// keep this local log for event_id correlation but suppress duplicate notification.
		c.log.With(
			sl.Err(err),
			slog.String("event_id", params.EventId),
			slog.String("order_id", params.OrderId),
			slog.Bool("tg_skip", true),
		).Error("register invoice")
		if c.retryQueue != nil {
			c.retryQueue.Enqueue(params, err.Error())
		}
		return nil
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
	return payment
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

	// Idempotency guard: if the order already records an invoice, verify it still
	// exists on the wFirma side before deciding. The check is authoritative because
	// an invoice can be deleted in wFirma while the local reference lingers.
	if params.InvoiceId != "" {
		exists, err := c.inv.InvoiceExists(ctx, params.InvoiceId)
		if err != nil {
			// State unknown — refuse to create rather than risk a duplicate.
			return nil, fmt.Errorf("verify existing invoice %s: %w", params.InvoiceId, err)
		}
		if exists {
			log.With(slog.String("invoice_id", params.InvoiceId)).Info("invoice already exists, skipping creation")
			return params, nil
		}
		// Confirmed deleted in wFirma — clear the stale reference and re-create.
		log.With(slog.String("invoice_id", params.InvoiceId)).Warn("recorded invoice missing in wfirma, re-creating")
		params.InvoiceId = ""
	}

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

	// When the order already carries a proforma (ProformaId/ProformaFile set), a new
	// proforma request means the order was changed and the old document is stale. Unlike
	// invoices, proformas can be deleted, so we fully discard the previous one — remove it
	// from wFirma, clear its reference in OpenCart, and delete the local PDF — before
	// creating a fresh proforma below. This logic is intentionally proforma-only.
	c.discardExistingProforma(ctx, params)

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

	// When the order was split into multiple wFirma documents, mirror the file
	// download for every part so the API can hand back all PDFs, not just the first.
	if err := c.downloadParts(ctx, payment); err != nil {
		return nil, err
	}

	return payment, nil
}

// discardExistingProforma removes a previously created proforma for an order before a new
// one is generated. It is a no-op when no proforma is recorded on params. Each step is
// best-effort and logged on failure rather than aborting: a leftover wFirma document, a
// stale OpenCart reference, or an orphaned file must not block re-issuing the proforma.
// On return params.ProformaId/ProformaFile are cleared so the caller creates and downloads
// a fresh document. Applies to proformas only — invoices are never deleted.
func (c *Core) discardExistingProforma(ctx context.Context, params *entity.CheckoutParams) {
	if params.ProformaId == "" && params.ProformaFile == "" {
		return
	}

	log := c.log.With(
		slog.String("order_id", params.OrderId),
		slog.String("proforma_id", params.ProformaId),
		slog.String("file_name", params.ProformaFile),
	)

	// 1. Delete the proforma document in wFirma (the client refuses non-proforma types).
	if params.ProformaId != "" {
		if err := c.inv.DeleteProforma(ctx, params.ProformaId); err != nil {
			log.Warn("delete proforma in wfirma", sl.Err(err))
		}
	}

	// 2. Clear the stored proforma reference in the OpenCart order so it never points at a
	// deleted document, even if creating the replacement below fails.
	if c.oc != nil && params.OrderId != "" {
		if orderId, err := strconv.ParseInt(params.OrderId, 10, 64); err == nil {
			if err := c.oc.UpdateOrderWithProforma(orderId, "", ""); err != nil {
				log.Warn("clear proforma in opencart", sl.Err(err))
			}
		} else {
			log.Warn("parse order id to clear proforma", sl.Err(err))
		}
	}

	// 3. Remove the local PDF file.
	if params.ProformaFile != "" {
		fileName := filepath.Join(c.filePath, params.ProformaFile)
		if _, err := os.Stat(fileName); err == nil {
			if err := os.Remove(fileName); err != nil {
				log.With(slog.String("path", c.filePath)).Warn("remove proforma file", sl.Err(err))
			}
		}
	}

	params.ProformaId = ""
	params.ProformaFile = ""
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

	if err := c.downloadParts(ctx, payment); err != nil {
		return nil, err
	}

	return payment, nil
}

// downloadParts fetches the PDF for every additional split part on the payment
// and fills in Link/InvoiceFile in place. The first part is assumed to already
// carry its own file fields (downloaded by the caller); the slice is iterated
// uniformly because Parts mirrors the first part as its head element.
func (c *Core) downloadParts(ctx context.Context, payment *entity.Payment) error {
	if payment == nil || len(payment.Parts) <= 1 {
		return nil
	}
	for _, part := range payment.Parts {
		if part == nil {
			continue
		}
		if part.Id == payment.Id {
			part.Link = payment.Link
			part.InvoiceFile = payment.InvoiceFile
			continue
		}
		fileName, link, err := c.downloadInvoice(ctx, "", part.Id)
		if err != nil {
			return err
		}
		part.Link = link
		part.InvoiceFile = fileName
	}
	return nil
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

// StripeCaptureAmount captures a held payment. It returns the checkout params (resolved
// from the session) alongside the payment so handlers can log the OpenCart order id even
// when the capture fails.
func (c *Core) StripeCaptureAmount(sessionId string, amount int64) (*entity.Payment, *entity.CheckoutParams, error) {
	pm, params, err := c.sc.CaptureAmount(sessionId, amount)
	if err != nil {
		return nil, params, err
	}
	if c.oc != nil && pm.OrderId != "" {
		if saveErr := c.oc.SavePaymentData(pm.OrderId, pm.Id, sessionId, "paid", pm.Amount); saveErr != nil {
			c.log.With(sl.Err(saveErr), slog.String("order_id", pm.OrderId)).Error("update payment status after capture")
		}
	}
	// Register the wFirma invoice asynchronously so the capture HTTP response is not
	// blocked by wFirma latency; failures fall through to the retry queue. A manual
	// capture emits no Stripe webhook we handle, so this is the only invoice trigger
	// for held-then-captured payments.
	if params != nil {
		go c.processInvoice(context.Background(), params)
	}
	return pm, params, nil
}

// ReconcileQueue returns the current set of unresolved held payments the reconciler is
// watching — holds that have a PaymentIntent but no invoice yet and have not been closed.
// It is a read-only snapshot for inspecting the queue, capped at the same batch limit the
// reconciler uses per tick.
func (c *Core) ReconcileQueue() ([]*entity.HeldPaymentSummary, error) {
	if c.db == nil {
		return nil, fmt.Errorf("payment database not connected")
	}
	params, err := c.db.GetUnresolvedHeldParams(reconcileBatchLimit)
	if err != nil {
		return nil, err
	}
	items := make([]*entity.HeldPaymentSummary, 0, len(params))
	for _, p := range params {
		items = append(items, &entity.HeldPaymentSummary{
			OrderId:   p.OrderId,
			PaymentId: p.PaymentId,
			SessionId: p.SessionId,
			Total:     p.Total,
			Currency:  p.Currency,
			Created:   p.Created,
		})
	}
	return items, nil
}

func (c *Core) StripePaymentStatus(orderId string) (*entity.PaymentStatus, error) {
	if c.sc == nil {
		return nil, fmt.Errorf("stripe service not connected")
	}
	return c.sc.PaymentStatus(orderId)
}

// StripeCancelPayment cancels a held payment. It returns the checkout params (resolved
// from the session) alongside the payment so handlers can log the OpenCart order id even
// when the cancellation fails.
func (c *Core) StripeCancelPayment(sessionId, reason string) (*entity.Payment, *entity.CheckoutParams, error) {
	pm, params, err := c.sc.CancelPayment(sessionId, reason)
	if err != nil {
		return nil, params, err
	}
	if c.oc != nil && pm.OrderId != "" {
		if saveErr := c.oc.SavePaymentData(pm.OrderId, pm.Id, sessionId, "canceled", pm.Amount); saveErr != nil {
			c.log.With(sl.Err(saveErr), slog.String("order_id", pm.OrderId)).Error("update payment status after cancel")
		}
	}
	return pm, params, nil
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
