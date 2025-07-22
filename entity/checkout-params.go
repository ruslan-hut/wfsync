package entity

import (
	"encoding/json"
	"fmt"
	"github.com/biter777/countries"
	"github.com/stripe/stripe-go/v76"
	"net/http"
	"time"
	"wfsync/lib/validate"
)

type Source string

const (
	SourceApi      Source = "api"
	SourceStripe   Source = "stripe"
	SourceOpenCart Source = "opencart"
)

type CheckoutParams struct {
	ClientDetails *ClientDetails `json:"client_details" bson:"client_details" validate:"required"`
	LineItems     []*LineItem    `json:"line_items" bson:"line_items" validate:"required,min=1,dive"`
	Total         int64          `json:"total" bson:"total" validate:"required,min=1"`
	Currency      string         `json:"currency" bson:"currency" validate:"required,oneof=PLN EUR"`
	CurrencyValue float64        `json:"currency_value,omitempty" bson:"currency_value,omitempty"`
	OrderId       string         `json:"order_id" bson:"order_id" validate:"required,min=1,max=32"`
	SuccessUrl    string         `json:"success_url" bson:"success_url" validate:"required,url"`
	Created       time.Time      `json:"created" bson:"created"`
	Closed        time.Time      `json:"closed,omitempty" bson:"closed"`
	Status        string         `json:"status" bson:"status"`
	SessionId     string         `json:"session_id,omitempty" bson:"session_id,omitempty"`
	InvoiceId     string         `json:"invoice_id,omitempty" bson:"invoice_id,omitempty"`
	InvoiceFile   string         `json:"invoice_file,omitempty" bson:"invoice_file,omitempty"`
	ProformaId    string         `json:"proforma_id,omitempty" bson:"proforma_id,omitempty"`
	ProformaFile  string         `json:"proforma_file,omitempty" bson:"proforma_file,omitempty"`
	Paid          bool           `json:"paid,omitempty" bson:"paid"`
	Source        Source         `json:"source,omitempty" bson:"source"`
	Payload       interface{}    `json:"payload,omitempty" bson:"payload,omitempty"`
}

func (c *CheckoutParams) Bind(_ *http.Request) error {
	c.Created = time.Now()
	return validate.Struct(c)
}

func (c *CheckoutParams) ValidateTotal() error {
	var total int64
	for _, item := range c.LineItems {
		total += item.Qty * item.Price
	}
	if c.Total == total {
		return nil
	}
	return fmt.Errorf("total amount %d does not match sum of line items %d", c.Total, total)
}

func (c *CheckoutParams) Validate() error {
	if len(c.LineItems) == 0 {
		return fmt.Errorf("no line items")
	}
	if c.ClientDetails == nil {
		return fmt.Errorf("no client details")
	}
	//err := c.ValidateTotal()
	//if err != nil {
	//	return err
	//}
	return nil
}

func (c *CheckoutParams) RefineTotal(count int) error {
	var linesTotal int64
	for _, item := range c.LineItems {
		linesTotal += item.Qty * item.Price
	}
	if linesTotal == c.Total {
		return nil
	}
	if count > 10 {
		return fmt.Errorf("too many refinements")
	}
	diff := c.Total - linesTotal
	for _, item := range c.LineItems {
		if item.Price > 0 {
			if diff > 0 {
				item.Price++
				diff--
			} else {
				item.Price--
				diff++
			}
		}
		if diff == 0 {
			break
		}
	}
	count++
	return c.RefineTotal(count)
}

func (c *CheckoutParams) AddShipping(title string, amount int64) {
	c.LineItems = append(c.LineItems, ShippingLineItem(title, amount))
}

type LineItem struct {
	Name  string `json:"name" validate:"required"`
	Qty   int64  `json:"qty" validate:"required,min=1"`
	Price int64  `json:"price" validate:"required,min=1"`
	Sku   string `json:"sku,omitempty" bson:"sku"`
}

func ShippingLineItem(title string, amount int64) *LineItem {
	if title == "" {
		title = "Zwrot kosztów transportu towarów"
	} else {
		title = fmt.Sprintf("Zwrot kosztów transportu towarów (%s)", title)
	}
	return &LineItem{
		Name:  title,
		Qty:   1,
		Price: amount,
	}
}

type ClientDetails struct {
	Name    string `json:"name" bson:"name" validate:"required"`
	Email   string `json:"email" bson:"email" validate:"required,email"`
	Phone   string `json:"phone" bson:"phone"`
	Country string `json:"country" bson:"country"`
	ZipCode string `json:"zip_code" bson:"zip_code"`
	City    string `json:"city" bson:"city"`
	Street  string `json:"street" bson:"street"`
	TaxId   string `json:"tax_id" bson:"tax_id"`
}

func (c *ClientDetails) CountryCode() string {
	if c.Country == "" {
		return ""
	}
	if len(c.Country) == 2 {
		return c.Country
	}
	country := countries.ByName(c.Country)
	code := country.Alpha2()
	if len(code) == 2 {
		return code
	}
	return ""
}

// ParseTaxId extracts a tax ID from a JSON-formatted string based on the given field ID and assigns it to the ClientDetails.
// Returns an error if the provided raw data is invalid JSON or the extraction fails.
// Raw string example: {"2":"DE362155758"}
func (c *ClientDetails) ParseTaxId(fieldId, raw string) error {
	if fieldId == "" || raw == "" {
		return nil
	}
	//var jsonStr string
	//if err := json.Unmarshal([]byte(raw), &jsonStr); err != nil {
	//	return err
	//}
	var data map[string]string
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return err
	}
	c.TaxId = data[fieldId]
	return nil
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
		params.AddShipping("", sess.ShippingCost.AmountTotal)
	}
	if sess.Metadata != nil {
		id, ok := sess.Metadata["order_id"]
		if ok {
			params.OrderId = id
		}
	}
	if params.OrderId == "" {
		params.OrderId = sess.ID
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
		params.AddShipping("", inv.ShippingCost.AmountTotal)
	}
	if inv.Metadata != nil {
		id, ok := inv.Metadata["order_id"]
		if ok {
			params.OrderId = id
		}
	}
	if params.OrderId == "" {
		params.OrderId = inv.ID
	}
	return params
}
