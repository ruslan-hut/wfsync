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
}

func (p *Payment) Bind(_ *http.Request) error {
	return validate.Struct(p)
}
