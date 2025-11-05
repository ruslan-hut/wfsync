package stripeclient

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
	"wfsync/entity"
	"wfsync/internal/config"
	"wfsync/lib/sl"

	"github.com/stripe/stripe-go/v76"
	"github.com/stripe/stripe-go/v76/client"
)

type Database interface {
	Save(key string, value interface{}) error
	SaveCheckoutParams(params *entity.CheckoutParams) error
	GetCheckoutParamsForEvent(eventId string) (*entity.CheckoutParams, error)
	GetCheckoutParamsSession(sessionId string) (*entity.CheckoutParams, error)
}

type StripeClient struct {
	sc            *client.API
	webhookSecret string
	successUrl    string
	db            Database
	log           *slog.Logger
	mutex         sync.Mutex
	testMode      bool
}

func New(conf *config.Config, logger *slog.Logger) *StripeClient {
	stripeKey := conf.Stripe.APIKey
	webhookSecret := conf.Stripe.WebhookSecret
	if conf.Stripe.TestMode {
		stripeKey = conf.Stripe.TestKey
		webhookSecret = conf.Stripe.TestWebhookSecret
		logger.With(
			sl.Secret("api_key", stripeKey),
			sl.Secret("webhook_secret", webhookSecret),
		).Info("using test mode for stripe")
	}
	sc := &client.API{}
	sc.Init(stripeKey, nil)
	return &StripeClient{
		sc:            sc,
		webhookSecret: webhookSecret,
		successUrl:    conf.Stripe.SuccessURL,
		testMode:      conf.Stripe.TestMode,
		log:           logger.With(sl.Module("stripe")),
	}
}

func (s *StripeClient) SetDatabase(db Database) {
	s.db = db
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
		s.log.With(
			sl.Secret("secret", secret),
		).Warn("signature mismatch")
		if s.testMode {
			return true
		}
	}
	return isValid
}

func (s *StripeClient) HandleEvent(evt *stripe.Event) *entity.CheckoutParams {
	switch evt.Type {
	case stripe.EventTypeCheckoutSessionCompleted:
		return s.handleCheckoutCompleted(evt)
	case stripe.EventTypeInvoiceFinalized:
		return s.handleInvoiceFinalized(evt)
	case stripe.EventTypePaymentIntentAmountCapturableUpdated:
		return s.handleAmountCapturable(evt)
	default:
		return nil
	}
}

func (s *StripeClient) handleCheckoutCompleted(evt *stripe.Event) *entity.CheckoutParams {
	invID := evt.GetObjectValue("id")
	log := s.log.With(
		slog.Any("event_type", evt.Type),
		slog.String("event_id", evt.ID),
		slog.String("session_id", invID),
	)

	params, _ := s.db.GetCheckoutParamsForEvent(evt.ID)
	if params != nil && params.OrderId != "" {
		log.With(
			slog.String("order_id", params.OrderId),
		).Info("checkout params found in database")
		return params
	}

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

	params = entity.NewFromCheckoutSession(sess)
	params.EventId = evt.ID

	err = s.db.SaveCheckoutParams(params)
	if err != nil {
		log.With(
			sl.Err(err),
		).Error("save checkout params to database")
	}

	return params
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

func (s *StripeClient) handleAmountCapturable(evt *stripe.Event) *entity.CheckoutParams {
	invID := evt.GetObjectValue("id")
	log := s.log.With(
		slog.Any("event_type", evt.Type),
		slog.String("event_id", evt.ID),
		slog.String("id", invID),
	)
	pi, err := s.sc.PaymentIntents.Get(invID, nil)
	if err != nil {
		log.With(
			sl.Err(err),
		).Error("get payment intent from stripe")
	}
	log.With(
		slog.Int64("amount", pi.Amount),
		slog.String("currency", string(pi.Currency)),
		slog.String("status", string(pi.Status)),
	).Debug("fetching payment intent from stripe")
	return nil
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

	csParams := s.sessionParamsFromCheckout(params)
	csParams.PaymentIntentData = &stripe.CheckoutSessionPaymentIntentDataParams{
		CaptureMethod: stripe.String("manual"),
	}

	cs, err := s.sc.CheckoutSessions.New(csParams)
	if err != nil {
		err = s.parseErr(err)
		return nil, fmt.Errorf("stripe response: %w", err)
	}
	//log = log.With(slog.String("session_id", cs.ID))

	params.Payload = cs
	params.SessionId = cs.ID
	params.Status = string(cs.Status)

	payment := &entity.Payment{
		Id:      cs.ID,
		OrderId: params.OrderId,
		Amount:  params.Total,
		Link:    cs.URL,
	}

	log.Info("hold link created")
	return payment, nil
}

func (s *StripeClient) CaptureAmount(sessionId string, amount int64) (*entity.Payment, error) {
	log := s.log.With(
		slog.Int64("amount", amount),
		slog.String("session_id", sessionId),
	)

	params, err := s.db.GetCheckoutParamsSession(sessionId)
	if err != nil {
		return nil, fmt.Errorf("failed to get checkout params from database: %w", err)
	}
	if params == nil {
		return nil, fmt.Errorf("checkout params not found in database")
	}
	if params.PaymentId == "" {
		return nil, fmt.Errorf("payment id not found in checkout params")
	}
	if amount == 0 {
		amount = params.Total
	}

	log = log.With(
		slog.String("currency", params.Currency),
		slog.String("order_id", params.OrderId),
	)

	captureParams := &stripe.PaymentIntentCaptureParams{
		AmountToCapture: stripe.Int64(amount),
	}

	result, err := s.sc.PaymentIntents.Capture(params.PaymentId, captureParams)
	if err != nil {
		err = s.parseErr(err)
		return nil, fmt.Errorf("stripe response: %w", err)
	}

	payment := &entity.Payment{
		Id:      result.ID,
		OrderId: params.OrderId,
		Amount:  result.Amount,
	}

	log.Info("capture amount successful")
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

	if params.ClientDetails.Email == "" {
		return nil, fmt.Errorf("missing email address")
	}
	log = log.With(slog.String("email", params.ClientDetails.Email))

	csParams := s.sessionParamsFromCheckout(params)

	cs, err := s.sc.CheckoutSessions.New(csParams)
	if err != nil {
		err = s.parseErr(err)
		return nil, fmt.Errorf("stripe checkout session: %w", err)
	}
	//log = log.With(slog.String("session_id", cs.ID))

	params.Payload = cs
	params.SessionId = cs.ID
	params.Status = string(cs.Status)

	payment := &entity.Payment{
		Id:      cs.ID,
		OrderId: params.OrderId,
		Amount:  params.Total,
		Link:    cs.URL,
	}

	log.Info("payment link created")
	return payment, nil
}

func (s *StripeClient) sessionParamsFromCheckout(pm *entity.CheckoutParams) *stripe.CheckoutSessionParams {
	var lineItems []*stripe.CheckoutSessionLineItemParams
	for _, item := range pm.LineItems {
		lineItems = append(lineItems, &stripe.CheckoutSessionLineItemParams{
			PriceData: &stripe.CheckoutSessionLineItemPriceDataParams{
				Currency: stripe.String(pm.Currency),
				ProductData: &stripe.CheckoutSessionLineItemPriceDataProductDataParams{
					Name: stripe.String(item.Name),
				},
				UnitAmount: stripe.Int64(item.Price),
			},
			Quantity: stripe.Int64(item.Qty),
		})
	}
	return &stripe.CheckoutSessionParams{
		Mode:          stripe.String(string(stripe.CheckoutSessionModePayment)),
		LineItems:     lineItems,
		Metadata:      map[string]string{"order_id": pm.OrderId},
		SuccessURL:    stripe.String(s.successUrl),
		CustomerEmail: stripe.String(strings.TrimSpace(pm.ClientDetails.Email)),
	}
}
