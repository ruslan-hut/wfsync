package entity

import (
	"net/http"
	"wfsync/lib/validate"
)

type CheckoutParams struct {
	LineItems []*LineItem `json:"line_items" validate:"required,dive,min=1"`
	Total     int64       `json:"total" validate:"required,min=1"`
}

func (c *CheckoutParams) Bind(_ *http.Request) error {
	return validate.Struct(c)
}

type LineItem struct {
	Name  string `json:"name" validate:"required"`
	Qty   int64  `json:"qty" validate:"required,min=1"`
	Price int64  `json:"price" validate:"required,min=1"`
}
