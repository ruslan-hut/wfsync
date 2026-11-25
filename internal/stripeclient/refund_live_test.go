package stripeclient

import (
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
	"wfsync/entity"

	"github.com/stripe/stripe-go/v76"
	"github.com/stripe/stripe-go/v76/client"
)

type memDb struct {
	p map[string]*entity.CheckoutParams
}

func (m *memDb) Save(string, interface{}) error                  { return nil }
func (m *memDb) SaveCheckoutParams(*entity.CheckoutParams) error { return nil }
func (m *memDb) GetCheckoutParamsForEvent(string) (*entity.CheckoutParams, error) {
	return nil, nil
}
func (m *memDb) GetCheckoutParamsSession(s string) (*entity.CheckoutParams, error) {
	return m.p[s], nil
}
func (m *memDb) GetCheckoutParamsByOrder(string) (*entity.CheckoutParams, error) { return nil, nil }

// TestRefundLive runs the refund flow against Stripe test mode: refusal on an uncaptured
// hold, two partial refunds, a replay with the same idempotency key, an over-refund, the
// remainder, and the account-wide recent-refunds listing. Run with
// STRIPE_TEST_KEY=sk_test_... go test -run TestRefundLive -v ./internal/stripeclient
func TestRefundLive(t *testing.T) {
	key := os.Getenv("STRIPE_TEST_KEY")
	if key == "" {
		t.Skip("set STRIPE_TEST_KEY to run against Stripe test mode")
	}
	if !strings.HasPrefix(key, "sk_test_") && !strings.HasPrefix(key, "rk_test_") {
		t.Fatal("not a test-mode key")
	}
	sc := &client.API{}
	sc.Init(key, nil)
	db := &memDb{p: map[string]*entity.CheckoutParams{}}
	s := &StripeClient{sc: sc, db: db, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	start := time.Now().Add(-time.Minute)

	newPI := func(manual bool) string {
		p := &stripe.PaymentIntentParams{
			Amount: stripe.Int64(10000), Currency: stripe.String("pln"),
			PaymentMethod: stripe.String("pm_card_visa"), Confirm: stripe.Bool(true),
			PaymentMethodTypes: []*string{stripe.String("card")},
		}
		if manual {
			p.CaptureMethod = stripe.String("manual")
		}
		pi, err := sc.PaymentIntents.New(p)
		if err != nil {
			t.Fatal(err)
		}
		db.p["cs_"+pi.ID] = &entity.CheckoutParams{OrderId: "test_refund", PaymentId: pi.ID, SessionId: "cs_" + pi.ID, Total: 10000}
		return pi.ID
	}
	run := time.Now().UnixNano()
	k := func(s string) string { return "wfsync-test-" + s + "-" + time.Unix(0, run).Format("150405.000000000") }

	// hold without capture -> refused
	held := newPI(true)
	if _, _, err := s.Refund("cs_"+held, 0, "", "test", k("held")); err == nil {
		t.Fatal("refund of uncaptured hold accepted")
	} else {
		t.Log("held:", err)
	}
	_, _ = sc.PaymentIntents.Cancel(held, nil)

	pi := newPI(false)
	r1, _, err := s.Refund("cs_"+pi, 3000, "requested_by_customer", "test", k("p1"))
	if err != nil {
		t.Fatal(err)
	}
	r1b, _, err := s.Refund("cs_"+pi, 3000, "requested_by_customer", "test", k("p1"))
	if err != nil {
		t.Fatal(err)
	}
	if r1b.RefundId != r1.RefundId {
		t.Fatalf("same key gave two refunds: %s %s", r1.RefundId, r1b.RefundId)
	}
	t.Logf("partial 1: %s %d %s, replay -> %s", r1.RefundId, r1.Amount, r1.Status, r1b.RefundId)

	r2, _, err := s.Refund("cs_"+pi, 2000, "not completed", "test", k("p2"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("partial 2: %s %d", r2.RefundId, r2.Amount)

	if _, _, err = s.Refund("cs_"+pi, 6000, "", "test", k("over")); err == nil {
		t.Fatal("over-refund accepted")
	} else {
		t.Log("over:", err)
	}

	st, err := s.PaymentRefunds(pi)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("state: received %d refunded %d refunds %d -> %s", st.AmountReceived, st.AmountRefunded, len(st.Refunds), entity.RefundState(st.AmountReceived, st.AmountRefunded))
	if st.AmountRefunded != 5000 || len(st.Refunds) != 2 {
		t.Fatal("unexpected state")
	}

	r3, _, err := s.Refund("cs_"+pi, 0, "", "test", k("full"))
	if err != nil {
		t.Fatal(err)
	}
	st, _ = s.PaymentRefunds(pi)
	t.Logf("remainder: %s %d; state refunded %d -> %s", r3.RefundId, r3.Amount, st.AmountRefunded, entity.RefundState(st.AmountReceived, st.AmountRefunded))
	if r3.Amount != 5000 || st.AmountRefunded != 10000 {
		t.Fatal("remainder refund wrong")
	}

	ids, err := s.RecentRefunds(start)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, id := range ids {
		found = found || id == pi
	}
	t.Logf("recent refunds: %d payment intents, ours found: %v", len(ids), found)
	if !found {
		t.Fatal("RecentRefunds missed the payment")
	}
}
