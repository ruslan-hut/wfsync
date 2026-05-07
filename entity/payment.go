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
	// Parts carries every document produced for the order when the request was
	// split across multiple wFirma invoices (over the soft item limit).
	// Includes the first part as well, so consumers can iterate uniformly.
	// Empty when the order produced a single document.
	Parts []*Payment `json:"parts,omitempty"`
}

func (p *Payment) Bind(_ *http.Request) error {
	return validate.Struct(p)
}
