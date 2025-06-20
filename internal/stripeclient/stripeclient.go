package stripeclient

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/stripe/stripe-go/v76"
	"github.com/stripe/stripe-go/v76/client"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"wfsync/internal/wfirma"
	"wfsync/lib/sl"
)

type StripeClient struct {
	sc            *client.API
	webhookSecret string
	wfirma        *wfirma.Client
	log           *slog.Logger
}

func New(apiKey, whSecret string, wf *wfirma.Client, logger *slog.Logger) *StripeClient {
	sc := &client.API{}
	sc.Init(apiKey, nil)
	return &StripeClient{
		sc:            sc,
		webhookSecret: whSecret,
		wfirma:        wf,
		log:           logger.With(sl.Module("stripe")),
	}
}

func (s *StripeClient) VerifySignature(payload []byte, header string, tolerance time.Duration) bool {
	secret := s.webhookSecret
	parts := strings.Split(header, ",")
	var ts, sig string
	for _, p := range parts {
		if strings.HasPrefix(p, "t=") {
			ts = strings.TrimPrefix(p, "t=")
		}
		if strings.HasPrefix(p, "v1=") {
			sig = strings.TrimPrefix(p, "v1=")
		}
	}
	if ts == "" || sig == "" {
		s.log.Warn("missing timestamp or signature in header")
		return false
	}

	tsInt, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		s.log.With(
			slog.Any("error", err),
		).Warn("failed to parse timestamp")
		return false
	}

	eventTime := time.Unix(tsInt, 0)
	timeSince := time.Since(eventTime)
	if timeSince > tolerance {
		s.log.With(
			slog.Time("timestamp", eventTime),
			slog.Duration("age", timeSince),
			slog.Duration("tolerance", tolerance),
		).Warn("webhook timestamp too old")
		return false
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write(payload)
	expected := hex.EncodeToString(mac.Sum(nil))

	isValid := hmac.Equal([]byte(expected), []byte(sig))
	if !isValid {
		s.log.Warn("signature mismatch")
	}
	return isValid
}

func (s *StripeClient) HandleEvent(ctx context.Context, evt *stripe.Event) {
	log := s.log.With(
		slog.String("event_id", evt.ID),
		slog.Any("type", evt.Type),
	)
	switch evt.Type {
	case stripe.EventTypeCheckoutSessionCompleted:
		log.Info("handling checkout")
		s.handleCheckoutCompleted(ctx, evt)
	case stripe.EventTypeInvoiceFinalized:
		log.Info("handling invoice")
		s.handleInvoiceFinalized(ctx, evt)
	default:
		s.log.Debug("ignored event")
	}
}

func (s *StripeClient) handleCheckoutCompleted(ctx context.Context, evt *stripe.Event) {
	invID := evt.GetObjectValue("id")
	log := s.log.With(
		slog.String("session_id", invID),
	)
	t1 := time.Now()
	defer func() {
		t2 := time.Now()
		log.With(
			slog.String("duration", fmt.Sprintf("%.3fms", float64(t2.Sub(t1))/float64(time.Millisecond))),
		).Debug("session sync completed")
	}()

	sess, err := s.sc.CheckoutSessions.Get(invID, nil)
	if err != nil {
		log.With(
			slog.Any("error", err),
		).Error("get session from stripe")
		return
	}
	log = log.With(
		slog.String("customer_email", sess.CustomerEmail),
		slog.Int64("amount", sess.AmountTotal),
		slog.String("currency", string(sess.Currency)),
	)

	itemsIter := s.sc.CheckoutSessions.ListLineItems(&stripe.CheckoutSessionListLineItemsParams{
		Session: stripe.String(invID),
	})
	if itemsIter == nil {
		log.Error("items iterator is nil")
		return
	}
	lineItems := make([]*stripe.LineItem, 0)
	for itemsIter.Next() {
		lineItem := itemsIter.LineItem()
		lineItems = append(lineItems, lineItem)
	}
	if len(lineItems) == 0 {
		log.Error("no line items found")
		return
	}

	s.checkCustomer(sess)

	err = s.wfirma.SyncSession(ctx, sess, lineItems)
	if err != nil {
		log.With(
			slog.Any("error", err),
		).Error("sync with wfirma")
	}
}

func (s *StripeClient) handleInvoiceFinalized(ctx context.Context, evt *stripe.Event) {
	invID := evt.GetObjectValue("id")
	s.log.With(
		slog.String("invoice_id", invID),
	).Debug("fetching invoice from stripe")
	inv, err := s.sc.Invoices.Get(invID, nil)
	if err != nil {
		s.log.With(
			slog.Any("error", err),
		).Error("failed to get invoice from stripe")
		return
	}
	s.log.With(
		slog.String("invoice_id", invID),
		slog.Int64("amount", inv.AmountPaid),
	).Info("invoice fetched successfully")

	//h.log.With(
	//	slog.String("url", inv.InvoicePDF),
	//).Debug("fetching PDF")
	//pdfBuf, err := fetchPDF(inv.InvoicePDF)
	//if err != nil {
	//	h.log.With(
	//		slog.Any("error", err),
	//	).Error("failed to fetch PDF")
	//	return
	//}
	//h.log.With(
	//	slog.Int("size_bytes", len(pdfBuf)),
	//).Debug("PDF fetched successfully")
	//
	//h.log.With(
	//	slog.String("invoice_id", invID),
	//).Info("syncing invoice with wfirma")

	err = s.wfirma.SyncInvoice(ctx, inv, nil)
	if err != nil {
		s.log.With(
			slog.Any("error", err),
		).Error("failed to sync with wfirma")
	} else {
		s.log.With(
			slog.String("invoice_id", invID),
		).Info("invoice synced successfully with wfirma")
	}
}

//func fetchPDF(url string) ([]byte, error) {
//	resp, err := http.Get(url)
//	if err != nil {
//		return nil, err
//	}
//	defer resp.Body.Close()
//	return io.ReadAll(resp.Body)
//}

func (s *StripeClient) checkCustomer(sess *stripe.CheckoutSession) {
	if sess.Customer != nil {
		return
	}
	customer := &stripe.Customer{
		Email: sess.CustomerEmail,
	}
	if sess.Metadata != nil {
		s.log.With(
			slog.Any("metadata", sess.Metadata),
		).Debug("adding metadata to customer")
		customer.Name = sess.Metadata["name"]
	}
	sess.Customer = customer
}
