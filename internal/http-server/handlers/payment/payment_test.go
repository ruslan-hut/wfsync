package payment

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"wfsync/entity"

	"github.com/go-chi/chi/v5"
)

type fakeCore struct {
	Core
	gotReq *entity.RefundRequest
	gotKey string
	err    error
}

func (f *fakeCore) StripeRefund(_ string, req *entity.RefundRequest, _, key string) (*entity.RefundResult, *entity.CheckoutParams, error) {
	f.gotReq, f.gotKey = req, key
	if f.err != nil {
		return nil, nil, f.err
	}
	return &entity.RefundResult{Refund: &entity.Refund{RefundId: "re_1"}}, nil, nil
}

func TestRefundHandler(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		key        string
		coreErr    error
		wantStatus int
		wantAmount int64
		wantCalled bool
	}{
		{name: "full refund, no body", key: "k1", wantStatus: 200, wantCalled: true},
		{name: "partial refund", body: `{"amount":2500,"reason":"not completed"}`, key: "k2", wantStatus: 200, wantAmount: 2500, wantCalled: true},
		{name: "missing idempotency key", body: `{"amount":2500}`, wantStatus: 400},
		{name: "negative amount", body: `{"amount":-1}`, key: "k3", wantStatus: 400},
		{name: "key reused for another payment", key: "k4", coreErr: entity.ErrRefundKeyReused, wantStatus: 409, wantCalled: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			core := &fakeCore{err: tc.coreErr}
			h := Refund(slog.New(slog.NewTextHandler(io.Discard, nil)), core)

			req := httptest.NewRequest(http.MethodPost, "/refund/cs_1", strings.NewReader(tc.body))
			if tc.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			if tc.key != "" {
				req.Header.Set("Idempotency-Key", tc.key)
			}
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", "cs_1")
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
			rec := httptest.NewRecorder()
			h(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if (core.gotReq != nil) != tc.wantCalled {
				t.Fatalf("core called = %v, want %v", core.gotReq != nil, tc.wantCalled)
			}
			if tc.wantCalled && (core.gotReq.Amount != tc.wantAmount || core.gotKey != tc.key) {
				t.Fatalf("core got amount %d key %q", core.gotReq.Amount, core.gotKey)
			}
		})
	}
}
