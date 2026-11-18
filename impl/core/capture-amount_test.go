package core

import (
	"errors"
	"testing"
	"wfsync/entity"
)

func TestCheckCapturedAmount(t *testing.T) {
	cases := []struct {
		name     string
		total    int64 // invoice gross after the OpenCart overwrite
		captured int64
		wantErr  bool
	}{
		{name: "matches", total: 284400, captured: 284400},
		{name: "within tolerance above", total: 227624, captured: 227623},
		{name: "within tolerance below", total: 227622, captured: 227623},
		{name: "order 18234: OpenCart total, Zoho discount captured", total: 284400, captured: 227623, wantErr: true},
		{name: "just over tolerance", total: 227625, captured: 227623, wantErr: true},
		{name: "invoice below captured", total: 100000, captured: 120000, wantErr: true},
		{name: "partial capture of a full hold", total: 50000, captured: 30000, wantErr: true},
		{name: "partial capture matching an edited order", total: 30000, captured: 30000},
		{name: "captured amount unknown", total: 284400, captured: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkCapturedAmount(&entity.CheckoutParams{OrderId: "18234", Total: tc.total, AmountPaid: tc.captured})
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil && !errors.Is(err, errCaptureAmountMismatch) {
				t.Fatalf("err = %v, want errCaptureAmountMismatch", err)
			}
		})
	}
}

// TestAmountAlertsFirst pins the anti-spam rule: capture API, webhook and reconciler all
// see the same capture, but only the first report reaches Telegram.
func TestAmountAlertsFirst(t *testing.T) {
	a := &amountAlerts{}
	p := &entity.CheckoutParams{OrderId: "18234", Total: 284400, AmountPaid: 227623}
	if !a.first(p) {
		t.Fatal("first report suppressed")
	}
	if a.first(p) {
		t.Fatal("repeat report not suppressed")
	}
	if !a.first(&entity.CheckoutParams{OrderId: "18235", Total: 284400, AmountPaid: 227623}) {
		t.Fatal("another order suppressed")
	}
}

func TestFormatMinor(t *testing.T) {
	cases := map[int64]string{227623: "2276.23", 5: "0.05", 0: "0.00", -567: "-5.67"}
	for in, want := range cases {
		if got := formatMinor(in); got != want {
			t.Errorf("formatMinor(%d) = %q, want %q", in, got, want)
		}
	}
}
