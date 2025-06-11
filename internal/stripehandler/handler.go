package stripehandler

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"wfsync/internal/wfirma"

	"github.com/stripe/stripe-go/v76"
	"github.com/stripe/stripe-go/v76/client"
)

const (
	eventCheckoutCompleted = "checkout.session.completed"
	eventInvoiceFinalized  = "invoice.finalized"
)

type Handler struct {
	sc            *client.API
	webhookSecret string
	wfirma        *wfirma.Client
	log           *slog.Logger
}

func New(apiKey, whSecret string, wf *wfirma.Client, logger *slog.Logger) *Handler {
	sc := &client.API{}
	sc.Init(apiKey, nil)
	return &Handler{
		sc:            sc,
		webhookSecret: whSecret,
		wfirma:        wf,
		log:           logger.With(slog.String("pkg", "stripehandler")),
	}
}

func (h *Handler) HandleWebhook(w http.ResponseWriter, r *http.Request) {
	const tolerance = 5 * time.Minute
	h.log.With(
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
	).Debug("received stripe webhook")

	payload, err := io.ReadAll(r.Body)
	if err != nil {
		h.log.With(
			slog.Any("error", err),
		).Error("failed to read request body")
		http.Error(w, "read", http.StatusBadRequest)
		return
	}

	sig := r.Header.Get("Stripe-Signature")
	if !h.verifySignature(payload, sig, tolerance) {
		h.log.Error("invalid webhook signature")
		http.Error(w, "signature", http.StatusBadRequest)
		return
	}

	var evt stripe.Event
	if err := json.Unmarshal(payload, &evt); err != nil {
		h.log.With(
			slog.Any("error", err),
		).Error("unmarshal event")
		http.Error(w, "json", http.StatusBadRequest)
		return
	}

	log := h.log.With(
		slog.String("event_id", evt.ID),
		slog.Any("type", evt.Type),
	)

	switch evt.Type {
	case eventCheckoutCompleted:
		log.Info("handling checkout")
		h.handleCheckoutCompleted(context.Background(), &evt)
	case eventInvoiceFinalized:
		log.Info("handling invoice")
		h.handleInvoiceFinalized(context.Background(), &evt)
	default:
		log.Info("ignored event")
	}

	w.WriteHeader(http.StatusOK)
}

func (h *Handler) verifySignature(payload []byte, header string, tolerance time.Duration) bool {
	secret := h.webhookSecret
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
		h.log.Debug("missing timestamp or signature in header")
		return false
	}

	tsInt, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		h.log.With(
			slog.Any("error", err),
		).Debug("failed to parse timestamp")
		return false
	}

	eventTime := time.Unix(tsInt, 0)
	timeSince := time.Since(eventTime)
	if timeSince > tolerance {
		h.log.With(
			slog.Time("timestamp", eventTime),
			slog.Duration("age", timeSince),
			slog.Duration("tolerance", tolerance),
		).Debug("webhook timestamp too old")
		return false
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write(payload)
	expected := hex.EncodeToString(mac.Sum(nil))

	isValid := hmac.Equal([]byte(expected), []byte(sig))
	if !isValid {
		h.log.Debug("signature mismatch")
	}
	return isValid
}

func (h *Handler) handleCheckoutCompleted(ctx context.Context, evt *stripe.Event) {
	invID := evt.GetObjectValue("id")
	log := h.log.With(
		slog.String("session_id", invID),
	)
	t1 := time.Now()
	defer func() {
		t2 := time.Now()
		log.With(
			slog.String("duration", fmt.Sprintf("%.3fms", float64(t2.Sub(t1))/float64(time.Millisecond))),
		).Debug("session sync completed")
	}()

	sess, err := h.sc.CheckoutSessions.Get(invID, nil)
	if err != nil {
		log.With(
			slog.Any("error", err),
		).Error("get session from stripe")
		return
	}
	log.With(
		slog.String("customer_email", sess.CustomerEmail),
		slog.Int64("amount", sess.AmountTotal),
	).Info("session fetched successfully")

	err = h.wfirma.SyncSession(ctx, sess)
	if err != nil {
		log.With(
			slog.Any("error", err),
		).Error("sync with wfirma")
	}
}

func (h *Handler) handleInvoiceFinalized(ctx context.Context, evt *stripe.Event) {
	invID := evt.GetObjectValue("id")
	h.log.With(
		slog.String("invoice_id", invID),
	).Debug("fetching invoice from stripe")
	inv, err := h.sc.Invoices.Get(invID, nil)
	if err != nil {
		h.log.With(
			slog.Any("error", err),
		).Error("failed to get invoice from stripe")
		return
	}
	h.log.With(
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

	err = h.wfirma.SyncInvoice(ctx, inv, nil)
	if err != nil {
		h.log.With(
			slog.Any("error", err),
		).Error("failed to sync with wfirma")
	} else {
		h.log.With(
			slog.String("invoice_id", invID),
		).Info("invoice synced successfully with wfirma")
	}
}

func fetchPDF(url string) ([]byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}
