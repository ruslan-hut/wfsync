package core

import (
	"errors"
	"io"
	"log/slog"
	"testing"
	"wfsync/entity"
	"wfsync/internal/stripeclient"
)

type fakeRefundDb struct {
	refunds []*entity.Refund
	params  map[string]*entity.CheckoutParams
}

func (f *fakeRefundDb) SaveRefund(r *entity.Refund) (bool, error) {
	for _, x := range f.refunds {
		if x.RefundId == r.RefundId {
			return false, nil
		}
	}
	f.refunds = append(f.refunds, r)
	return true, nil
}

func (f *fakeRefundDb) GetRefundsByPayment(paymentId string) ([]*entity.Refund, error) {
	var out []*entity.Refund
	for _, r := range f.refunds {
		if r.PaymentId == paymentId {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeRefundDb) GetRefundByIdempotencyKey(key string) (*entity.Refund, error) {
	for _, r := range f.refunds {
		if r.IdempotencyKey == key {
			return r, nil
		}
	}
	return nil, nil
}

func (f *fakeRefundDb) GetCheckoutParamsSession(sessionId string) (*entity.CheckoutParams, error) {
	for _, p := range f.params {
		if p.SessionId == sessionId {
			return p, nil
		}
	}
	return nil, nil
}

func (f *fakeRefundDb) GetCheckoutParamsByPayment(paymentId string) (*entity.CheckoutParams, error) {
	return f.params[paymentId], nil
}

func newRefundCore(db *fakeRefundDb) *Core {
	return &Core{
		sc:       &stripeclient.StripeClient{},
		refundDb: db,
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// TestStripeRefundReplay pins the idempotency contract: a key already stored locally
// returns the earlier refund without calling Stripe, and a key from another payment is
// refused. The zero StripeClient would panic if Stripe were called.
func TestStripeRefundReplay(t *testing.T) {
	db := &fakeRefundDb{
		refunds: []*entity.Refund{
			{RefundId: "re_1", PaymentId: "pi_1", SessionId: "cs_1", Amount: 3000, Status: "succeeded", IdempotencyKey: "k1"},
			{RefundId: "re_2", PaymentId: "pi_1", SessionId: "cs_1", Amount: 2000, Status: "succeeded", IdempotencyKey: "k2"},
			{RefundId: "re_3", PaymentId: "pi_1", SessionId: "cs_1", Amount: 500, Status: "failed", IdempotencyKey: "k3"},
		},
		params: map[string]*entity.CheckoutParams{
			"pi_1": {OrderId: "18400", PaymentId: "pi_1", SessionId: "cs_1", Total: 10000, AmountPaid: 10000},
		},
	}
	c := newRefundCore(db)
	req := &entity.RefundRequest{Amount: 3000}

	res, _, err := c.StripeRefund("cs_1", req, "zoho.support", "k1")
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !res.Replayed || res.Refund.RefundId != "re_1" {
		t.Fatalf("replay = %+v, want replayed re_1", res)
	}
	if res.AmountRefunded != 5000 || res.PaymentStatus != entity.PaymentStatusPartiallyRefunded {
		t.Fatalf("totals = %d %s, want 5000 partially_refunded", res.AmountRefunded, res.PaymentStatus)
	}

	if _, _, err = c.StripeRefund("cs_other", req, "zoho.support", "k1"); !errors.Is(err, entity.ErrRefundKeyReused) {
		t.Fatalf("other session err = %v, want ErrRefundKeyReused", err)
	}
	if _, _, err = c.StripeRefund("cs_1", &entity.RefundRequest{Amount: 1000}, "zoho.support", "k1"); !errors.Is(err, entity.ErrRefundKeyReused) {
		t.Fatalf("other amount err = %v, want ErrRefundKeyReused", err)
	}
	if _, _, err = c.StripeRefund("cs_1", req, "zoho.support", ""); err == nil {
		t.Fatal("empty key accepted")
	}
}

// TestSettledStatus pins that a late "paid" write cannot hide a recorded refund.
func TestSettledStatus(t *testing.T) {
	db := &fakeRefundDb{refunds: []*entity.Refund{
		{RefundId: "re_1", PaymentId: "pi_part", Amount: 4000, Status: "succeeded"},
		{RefundId: "re_2", PaymentId: "pi_full", Amount: 10000, Status: "pending"},
		{RefundId: "re_3", PaymentId: "pi_failed", Amount: 10000, Status: "failed"},
	}}
	c := newRefundCore(db)
	cases := []struct {
		paymentId string
		want      string
	}{
		{"pi_none", entity.PaymentStatusPaid},
		{"pi_part", entity.PaymentStatusPartiallyRefunded},
		{"pi_full", entity.PaymentStatusRefunded},
		{"pi_failed", entity.PaymentStatusPaid},
	}
	for _, tc := range cases {
		t.Run(tc.paymentId, func(t *testing.T) {
			got := c.settledStatus(&entity.CheckoutParams{PaymentId: tc.paymentId, Total: 10000})
			if got != tc.want {
				t.Fatalf("settledStatus = %s, want %s", got, tc.want)
			}
		})
	}
}
