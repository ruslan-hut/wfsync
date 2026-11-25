package entity

import "testing"

func TestRefundState(t *testing.T) {
	cases := []struct {
		name               string
		received, refunded int64
		want               string
	}{
		{"nothing refunded", 10000, 0, PaymentStatusPaid},
		{"partial", 10000, 2500, PaymentStatusPartiallyRefunded},
		{"two partials make full", 10000, 10000, PaymentStatusRefunded},
		{"full of a partial capture", 7000, 7000, PaymentStatusRefunded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RefundState(tc.received, tc.refunded); got != tc.want {
				t.Fatalf("RefundState(%d, %d) = %s, want %s", tc.received, tc.refunded, got, tc.want)
			}
		})
	}
}
