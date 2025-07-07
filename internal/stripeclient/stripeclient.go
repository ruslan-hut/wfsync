package stripeclient

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/stripe/stripe-go/v76"
	"github.com/stripe/stripe-go/v76/client"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
	"wfsync/entity"
	"wfsync/internal/wfirma"
	"wfsync/lib/sl"
)

type Database interface {
	Save(key string, value interface{}) error
	SaveCheckoutParams(params *entity.CheckoutParams) error
}

type StripeClient struct {
	sc            *client.API
	webhookSecret string
	successUrl    string
	wfirma        *wfirma.Client
	db            Database
	log           *slog.Logger
	mutex         sync.Mutex
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

func (s *StripeClient) SetDatabase(db Database) {
	s.db = db
}

func (s *StripeClient) SetSuccessUrl(url string) {
	s.successUrl = url
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

func (s *StripeClient) HandleEvent(evt *stripe.Event) *entity.CheckoutParams {
	switch evt.Type {
	case stripe.EventTypeCheckoutSessionCompleted:
		return s.handleCheckoutCompleted(evt)
	case stripe.EventTypeInvoiceFinalized:
		return s.handleInvoiceFinalized(evt)
	default:
		return nil
	}
}

func (s *StripeClient) handleCheckoutCompleted(evt *stripe.Event) *entity.CheckoutParams {
	invID := evt.GetObjectValue("id")
	log := s.log.With(
		slog.Any("event_type", evt.Type),
		slog.String("session_id", invID),
	)

	sess, err := s.sc.CheckoutSessions.Get(invID, &stripe.CheckoutSessionParams{
		Expand: []*string{
			stripe.String("line_items"),
			stripe.String("shipping_cost"),
		},
	})
	if err != nil {
		log.With(
			sl.Err(err),
		).Error("get session from stripe")
		return nil
	}
	log = log.With(
		slog.String("customer_email", sess.CustomerEmail),
		slog.Int64("amount", sess.AmountTotal),
		slog.String("currency", string(sess.Currency)),
	)

	s.checkCustomer(sess)

	return entity.NewFromCheckoutSession(sess)
}

func (s *StripeClient) handleInvoiceFinalized(evt *stripe.Event) *entity.CheckoutParams {
	invID := evt.GetObjectValue("id")
	s.log.With(
		slog.Any("event_type", evt.Type),
		slog.String("invoice_id", invID),
	).Debug("fetching invoice from stripe")

	inv, err := s.sc.Invoices.Get(invID, nil)
	if err != nil {
		s.log.With(
			sl.Err(err),
		).Error("get invoice from stripe")
		return nil
	}
	return entity.NewFromInvoice(inv)
}

func (s *StripeClient) checkCustomer(sess *stripe.CheckoutSession) {
	customer := sess.Customer
	if customer == nil {
		customer = &stripe.Customer{
			Email: sess.CustomerEmail,
		}
	}
	if sess.CustomerDetails != nil {
		customer.Name = sess.CustomerDetails.Name
		customer.Email = sess.CustomerDetails.Email
		customer.Phone = sess.CustomerDetails.Phone
		customer.Address = sess.CustomerDetails.Address
		if sess.CustomerEmail == "" {
			sess.CustomerEmail = sess.CustomerDetails.Email
		}
	}
	sess.Customer = customer
}

func (s *StripeClient) HoldAmount(params *entity.CheckoutParams) (*entity.Payment, error) {
	log := s.log.With(
		slog.Int64("total", params.Total),
		slog.String("currency", params.Currency),
		slog.String("order_id", params.OrderId),
	)
	defer func() {
		err := s.db.SaveCheckoutParams(params)
		if err != nil {
			s.log.With(
				sl.Err(err),
			).Error("save checkout params to database")
		}
	}()

	successUrl := params.SuccessUrl
	if successUrl == "" {
		successUrl = s.successUrl
	}
	if successUrl == "" {
		return nil, fmt.Errorf("missing success url")
	}

	csParams := &stripe.CheckoutSessionParams{
		Mode: stripe.String(string(stripe.CheckoutSessionModePayment)),
		PaymentIntentData: &stripe.CheckoutSessionPaymentIntentDataParams{
			CaptureMethod: stripe.String("manual"),
		},
		LineItems: []*stripe.CheckoutSessionLineItemParams{
			{
				PriceData: &stripe.CheckoutSessionLineItemPriceDataParams{
					Currency: stripe.String(params.Currency),
					ProductData: &stripe.CheckoutSessionLineItemPriceDataProductDataParams{
						Name: stripe.String("Order " + params.OrderId),
					},
					UnitAmount: stripe.Int64(params.Total),
				},
				Quantity: stripe.Int64(1),
			},
		},
		Metadata:      map[string]string{"order_id": params.OrderId},
		SuccessURL:    stripe.String(successUrl),
		CustomerEmail: stripe.String(params.ClientDetails.Email),
	}

	cs, err := s.sc.CheckoutSessions.New(csParams)
	if err != nil {
		err = s.parseErr(err)
		log.With(
			sl.Err(err),
		).Error("create checkout session")
		return nil, fmt.Errorf("create checkout session: %w", err)
	}
	log = log.With(slog.String("session_id", cs.ID))

	params.Payload = cs
	params.SessionId = cs.ID
	params.Status = string(cs.Status)

	payment := &entity.Payment{
		Id:      cs.ID,
		OrderId: params.OrderId,
		Amount:  params.Total,
		Link:    cs.URL,
	}

	return payment, nil
}

func (s *StripeClient) PayAmount(params *entity.CheckoutParams) (*entity.Payment, error) {
	log := s.log.With(
		slog.Int64("total", params.Total),
		slog.String("currency", params.Currency),
		slog.String("order_id", params.OrderId),
	)
	defer func() {
		err := s.db.SaveCheckoutParams(params)
		if err != nil {
			s.log.With(
				sl.Err(err),
			).Error("save checkout params to database")
		}
	}()

	successUrl := params.SuccessUrl
	if successUrl == "" {
		successUrl = s.successUrl
	}
	if successUrl == "" {
		return nil, fmt.Errorf("missing success url")
	}

	csParams := &stripe.CheckoutSessionParams{
		Mode: stripe.String(string(stripe.CheckoutSessionModePayment)),
		LineItems: []*stripe.CheckoutSessionLineItemParams{
			{
				PriceData: &stripe.CheckoutSessionLineItemPriceDataParams{
					Currency: stripe.String(params.Currency),
					ProductData: &stripe.CheckoutSessionLineItemPriceDataProductDataParams{
						Name: stripe.String("Order " + params.OrderId),
					},
					UnitAmount: stripe.Int64(params.Total),
				},
				Quantity: stripe.Int64(1),
			},
		},
		Metadata:      map[string]string{"order_id": params.OrderId},
		SuccessURL:    stripe.String(successUrl),
		CustomerEmail: stripe.String(params.ClientDetails.Email),
	}

	cs, err := s.sc.CheckoutSessions.New(csParams)
	if err != nil {
		err = s.parseErr(err)
		log.With(
			sl.Err(err),
		).Error("create checkout session")
		return nil, fmt.Errorf("create checkout session: %w", err)
	}
	log = log.With(slog.String("session_id", cs.ID))

	params.Payload = cs
	params.SessionId = cs.ID
	params.Status = string(cs.Status)

	payment := &entity.Payment{
		Id:      cs.ID,
		OrderId: params.OrderId,
		Amount:  params.Total,
		Link:    cs.URL,
	}

	return payment, nil
}
