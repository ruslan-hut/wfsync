package entity

import (
	"fmt"
	"github.com/stripe/stripe-go/v76"
	"net/http"
	"time"
	"wfsync/lib/validate"
)

type Source string

const (
	SourceApi    Source = "api"
	SourceStripe Source = "stripe"
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
	InvoiceId     string         `json:"invoice_id,omitempty" bson:"invoice_id"`
	Paid          bool           `json:"paid,omitempty" bson:"paid"`
	Source        Source         `json:"source,omitempty" bson:"source"`
	Payload       interface{}    `json:"payload,omitempty" bson:"payload"`
}

func (c *CheckoutParams) Bind(_ *http.Request) error {
	c.Created = time.Now()
	return validate.Struct(c)
}

func (c *CheckoutParams) AddShipping(amount int64) {
	c.LineItems = append(c.LineItems, &LineItem{
		Name:  "Zwrot kosztów transportu towarów",
		Qty:   1,
		Price: amount,
	})
}

type LineItem struct {
	Name  string `json:"name" validate:"required"`
	Qty   int64  `json:"qty" validate:"required,min=1"`
	Price int64  `json:"price" validate:"required,min=1"`
	Sku   string `json:"sku,omitempty" bson:"sku"`
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

func NewFromCheckoutSession(sess *stripe.CheckoutSession) *CheckoutParams {
	params := &CheckoutParams{
		SessionId: sess.ID,
		Status:    string(sess.Status),
		Created:   time.Now(),
		Currency:  string(sess.Currency),
		Total:     sess.AmountTotal,
		Paid:      sess.PaymentStatus == stripe.CheckoutSessionPaymentStatusPaid,
		Payload:   sess,
		Source:    SourceStripe,
	}
	if sess.Customer != nil {
		client := &ClientDetails{
			Name:  sess.Customer.Name,
			Email: sess.Customer.Email,
			Phone: sess.Customer.Phone,
		}
		if sess.Customer.Address != nil {
			client.Country = sess.Customer.Address.Country
			client.ZipCode = sess.Customer.Address.PostalCode
			client.City = sess.Customer.Address.City
			client.Street = fmt.Sprintf("%s %s", sess.Customer.Address.Line1, sess.Customer.Address.Line2)
		}
		params.ClientDetails = client
	}
	if sess.LineItems != nil {
		for _, item := range sess.LineItems.Data {
			if item.Quantity == 0 {
				continue
			}
			lineItem := &LineItem{
				Name:  item.Description,
				Qty:   item.Quantity,
				Price: item.AmountTotal / item.Quantity,
			}
			params.LineItems = append(params.LineItems, lineItem)
		}
	}
	if sess.ShippingCost != nil && sess.ShippingCost.AmountTotal > 0 {
		params.AddShipping(sess.ShippingCost.AmountTotal)
	}
	if sess.Metadata != nil {
		id, ok := sess.Metadata["order_id"]
		if ok {
			params.OrderId = id
		}
	}
	return params
}

func NewFromInvoice(inv *stripe.Invoice) *CheckoutParams {
	params := &CheckoutParams{
		SessionId: inv.ID,
		Status:    string(inv.Status),
		Created:   time.Now(),
		Currency:  string(inv.Currency),
		Total:     inv.Total,
		Paid:      inv.Paid,
		Payload:   inv,
		Source:    SourceStripe,
	}
	if inv.Customer != nil {
		client := &ClientDetails{
			Name:  inv.Customer.Name,
			Email: inv.Customer.Email,
			Phone: inv.Customer.Phone,
		}
		if inv.Customer.Address != nil {
			client.Country = inv.Customer.Address.Country
			client.ZipCode = inv.Customer.Address.PostalCode
			client.City = inv.Customer.Address.City
			client.Street = fmt.Sprintf("%s %s", inv.Customer.Address.Line1, inv.Customer.Address.Line2)
		}
		params.ClientDetails = client
	}
	if inv.Lines != nil {
		for _, item := range inv.Lines.Data {
			if item.Quantity == 0 {
				continue
			}
			lineItem := &LineItem{
				Name:  item.Description,
				Qty:   item.Quantity,
				Price: item.Amount / item.Quantity,
			}
			params.LineItems = append(params.LineItems, lineItem)
		}
	}
	if inv.ShippingCost != nil && inv.ShippingCost.AmountTotal > 0 {
		params.AddShipping(inv.ShippingCost.AmountTotal)
	}
	if inv.Metadata != nil {
		id, ok := inv.Metadata["order_id"]
		if ok {
			params.OrderId = id
		}
	}
	return params
}
