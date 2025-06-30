package entity

import (
	"net/http"
	"wfsync/lib/validate"
)

type CheckoutParams struct {
	ClientDetails *ClientDetails `json:"client_details" validate:"required"`
	LineItems     []*LineItem    `json:"line_items" validate:"required,min=1,dive"`
	Total         int64          `json:"total" validate:"required,min=1"`
	Currency      string         `json:"currency" validate:"required,oneof=PLN EUR"`
	OrderId       string         `json:"order_id" validate:"required,min=1,max=32"`
}

func (c *CheckoutParams) Bind(_ *http.Request) error {
	return validate.Struct(c)
}

type LineItem struct {
	Name  string `json:"name" validate:"required"`
	Qty   int64  `json:"qty" validate:"required,min=1"`
	Price int64  `json:"price" validate:"required,min=1"`
}

type ClientDetails struct {
	Name    string `json:"name" validate:"required"`
	Email   string `json:"email" validate:"required,email"`
	Phone   string `json:"phone"`
	Country string `json:"country"`
	ZipCode string `json:"zip_code"`
	City    string `json:"city"`
	Street  string `json:"street"`
}
