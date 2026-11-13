package entity

import (
	"net/http"
	"wfsync/lib/validate"
)

type Payment struct {
	Amount      int64  `json:"amount"`
	Id          string `json:"id" validate:"required"`
	OrderId     string `json:"order_id" validate:"required"`
	Link        string `json:"link,omitempty"`
	InvoiceFile string `json:"invoice_file,omitempty"`
	// Number is the document's full wFirma number, e.g. "PRO 12/11/2026"; empty for
	// payments that are not wFirma documents or were recorded before it was kept.
	Number string `json:"number,omitempty"`
	// Date is the document's issue date, YYYY-MM-DD.
	Date string `json:"date,omitempty"`
	// Currency is the document's currency, ISO 4217 upper case; Amount is in its minor units.
	Currency string `json:"currency,omitempty"`
	// Parts carries every document produced for the order when the request was
	// split across multiple wFirma invoices (over the soft item limit).
	// Includes the first part as well, so consumers can iterate uniformly.
	// Empty when the order produced a single document.
	Parts []*Payment `json:"parts,omitempty"`
}

func (p *Payment) Bind(_ *http.Request) error {
	return validate.Struct(p)
}

// PaymentStatus reports the current Stripe state for an order. Status is the live
// Stripe status (PaymentIntent or CheckoutSession), Source records where that status
// was read from so callers can tell a live Stripe value from the locally stored one.
type PaymentStatus struct {
	OrderId        string `json:"order_id"`
	PaymentId      string `json:"payment_id,omitempty"`
	SessionId      string `json:"session_id,omitempty"`
	Status         string `json:"status"`
	Amount         int64  `json:"amount,omitempty"`
	AmountReceived int64  `json:"amount_received,omitempty"`
	Currency       string `json:"currency,omitempty"`
	Paid           bool   `json:"paid"`
	Captured       bool   `json:"captured"`
	InvoiceId      string `json:"invoice_id,omitempty"`
	Source         string `json:"source"`
}
