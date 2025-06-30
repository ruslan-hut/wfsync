package entity

import (
	"net/http"
	"time"
	"wfsync/lib/validate"
)

type CheckoutParams struct {
	ClientDetails *ClientDetails `json:"client_details" bson:"client_details" validate:"required"`
	LineItems     []*LineItem    `json:"line_items" bson:"line_items" validate:"required,min=1,dive"`
	Total         int64          `json:"total" bson:"total" validate:"required,min=1"`
	Currency      string         `json:"currency" bson:"currency" validate:"required,oneof=PLN EUR"`
	OrderId       string         `json:"order_id" bson:"order_id" validate:"required,min=1,max=32"`
	SuccessUrl    string         `json:"success_url" bson:"success_url" validate:"required,url"`
	Created       time.Time      `json:"created" bson:"created"`
	Closed        time.Time      `json:"closed,omitempty" bson:"closed"`
	Status        string         `json:"status" bson:"status"`
	SessionId     string         `json:"session_id,omitempty" bson:"session_id"`
}

func (c *CheckoutParams) Bind(_ *http.Request) error {
	c.Created = time.Now()
	return validate.Struct(c)
}

type LineItem struct {
	Name  string `json:"name" validate:"required"`
	Qty   int64  `json:"qty" validate:"required,min=1"`
	Price int64  `json:"price" validate:"required,min=1"`
}

type ClientDetails struct {
	Name    string `json:"name" bson:"name" validate:"required"`
	Email   string `json:"email" bson:"email" validate:"required,email"`
	Phone   string `json:"phone" bson:"phone"`
	Country string `json:"country" bson:"country"`
	ZipCode string `json:"zip_code" bson:"zip_code"`
	City    string `json:"city" bson:"city"`
	Street  string `json:"street" bson:"street"`
}
