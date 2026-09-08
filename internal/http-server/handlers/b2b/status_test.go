package b2b

import (
	"testing"
	"time"

	"wfsync/entity"
)

// TestToStatusPaidFlag covers the one field the portal acts on. Paid must be true only
// when the order is settled AND the match rests on a stated document number or a person's
// confirmation — telling a customer their order is paid on a guess is worse than saying
// nothing.
func TestToStatusPaidFlag(t *testing.T) {
	tests := []struct {
		name     string
		fact     entity.PaymentFact
		wantPaid bool
	}{
		{
			name:     "exact match, fully paid",
			fact:     entity.PaymentFact{Status: entity.PaymentFactPaid, MatchLevel: entity.MatchExact},
			wantPaid: true,
		},
		{
			name:     "confirmed by hand",
			fact:     entity.PaymentFact{Status: entity.PaymentFactPaid, MatchLevel: entity.MatchManual},
			wantPaid: true,
		},
		{
			name:     "overpaid still counts as paid",
			fact:     entity.PaymentFact{Status: entity.PaymentFactOverpaid, MatchLevel: entity.MatchExact},
			wantPaid: true,
		},
		{
			// Tied by amount and counterparty only — the portal must not present this
			// as settled.
			name:     "probable match is not shown as paid",
			fact:     entity.PaymentFact{Status: entity.PaymentFactPaid, MatchLevel: entity.MatchProbable},
			wantPaid: false,
		},
		{
			name:     "partial payment",
			fact:     entity.PaymentFact{Status: entity.PaymentFactPartial, MatchLevel: entity.MatchExact},
			wantPaid: false,
		},
		{
			// A EUR document settled in PLN: identified correctly, but the sums are not
			// comparable without a rate.
			name: "cross-currency awaits confirmation",
			fact: entity.PaymentFact{
				Status: entity.PaymentFactPartial, AmountStatus: entity.AmountFX,
				MatchLevel: entity.MatchExact, CurrencyDoc: "EUR", CurrencyPaid: "PLN",
			},
			wantPaid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := toStatus(&tt.fact).Paid; got != tt.wantPaid {
				t.Errorf("Paid = %v, want %v", got, tt.wantPaid)
			}
		})
	}
}

// TestUnpaidStatus covers an order with no payment on record: not an error, just unpaid.
func TestUnpaidStatus(t *testing.T) {
	s := unpaidStatus("order-uid-1")
	if s.Paid || s.Status != entity.PaymentFactUnpaid || s.OrderUID != "order-uid-1" {
		t.Errorf("unexpected status: %+v", s)
	}
}

func TestStatusBatchRequestValidation(t *testing.T) {
	tests := []struct {
		name    string
		req     statusBatchRequest
		wantErr bool
	}{
		{"one order", statusBatchRequest{OrderUIDs: []string{"a"}}, false},
		{"empty", statusBatchRequest{}, true},
		{"over the cap", statusBatchRequest{OrderUIDs: make([]string, statusBatchLimit+1)}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.req.Bind(nil)
			if (err != nil) != tt.wantErr {
				t.Errorf("Bind() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestToStatusFormatsDates checks the fields the portal renders and polls on.
func TestToStatusFormatsDates(t *testing.T) {
	paid := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	updated := time.Date(2026, 9, 1, 10, 30, 0, 0, time.UTC)

	s := toStatus(&entity.PaymentFact{PaidAt: paid, UpdatedAt: updated})
	if s.PaidAt != "2026-08-31" {
		t.Errorf("PaidAt = %q, want 2026-08-31", s.PaidAt)
	}
	if s.UpdatedAt != updated.Format(time.RFC3339) {
		t.Errorf("UpdatedAt = %q, want RFC3339", s.UpdatedAt)
	}

	// A record with no payment yet must not render zero dates.
	empty := toStatus(&entity.PaymentFact{})
	if empty.PaidAt != "" || empty.UpdatedAt != "" {
		t.Errorf("zero times rendered: %+v", empty)
	}
}
