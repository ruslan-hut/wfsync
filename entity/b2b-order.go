package entity

import (
	"errors"
	"math"
	"net/http"
	"strings"
	"time"
	"wfsync/lib/validate"
)

const (
	SourceB2B               Source = "b2b"
	DefaultCustomerGroupB2B        = 6
)

// ErrVATRateMismatch signals that the VAT rate implied by an order payload does
// not match the rate our internal rules require. Handlers should surface it as a
// 400 validation error so the calling system can reconcile its VAT calculation.
var ErrVATRateMismatch = errors.New("vat rate mismatch")

type B2BOrder struct {
	OrderUID        string     `json:"order_uid" validate:"required"`
	OrderNumber     string     `json:"order_number" validate:"required"`
	ClientUID       string     `json:"client_uid"`
	ClientName      string     `json:"client_name" validate:"required"`
	ClientEmail     string     `json:"client_email" validate:"required,email"`
	ClientPhone     string     `json:"client_phone"`
	ClientVAT       string     `json:"client_vat"`
	ClientCountry   string     `json:"client_country" validate:"required"`
	ClientCity      string     `json:"client_city"`
	ClientAddress   string     `json:"client_address"`
	ClientZipcode   string     `json:"client_zipcode"`
	BillingCountry  string     `json:"billing_country"`
	BillingCity     string     `json:"billing_city"`
	BillingAddress  string     `json:"billing_address"`
	BillingZipcode  string     `json:"billing_zipcode"`
	StoreUID        string     `json:"store_uid"`
	Status          string     `json:"status"`
	Total           float64    `json:"total" validate:"required,gt=0"`
	Subtotal        float64    `json:"subtotal"`
	TotalVAT        float64    `json:"total_vat"`
	DiscountPercent float64    `json:"discount_percent"`
	DiscountAmount  float64    `json:"discount_amount"`
	Shipment        float64    `json:"shipment"`
	CurrencyCode    string     `json:"currency_code" validate:"required,oneof=PLN EUR USD"`
	CreatedAt       time.Time  `json:"created_at"`
	Items           []*B2BItem `json:"items" validate:"required,min=1,dive"`
}

type B2BItem struct {
	ProductUID    string  `json:"product_uid"`
	ProductSKU    string  `json:"product_sku"`
	ProductName   string  `json:"product_name" validate:"required"`
	Quantity      int64   `json:"quantity" validate:"required,min=1"`
	Price         float64 `json:"price" validate:"required,gt=0"`
	Discount      float64 `json:"discount"`
	PriceDiscount float64 `json:"price_discount"`
	Tax           float64 `json:"tax"`
	Total         float64 `json:"total"`
}

func (o *B2BOrder) Bind(_ *http.Request) error {
	return validate.Struct(o)
}

// ToCheckoutParams converts B2BOrder to CheckoutParams format.
//
// The invoice contractor address defaults to the client's (delivery) address,
// but each billing_* field takes precedence when present — it reflects the
// client's official/registered address, which is what wFirma invoices must show
// rather than a shipping destination. The fallback is per-field: a payload that
// omits billing_country must not leave the country empty, since CountryCode()
// drives VAT code resolution and OSS detection, not just the printed address.
func (o *B2BOrder) ToCheckoutParams() *CheckoutParams {
	country := firstNonEmpty(o.BillingCountry, o.ClientCountry)
	city := firstNonEmpty(o.BillingCity, o.ClientCity)
	street := firstNonEmpty(o.BillingAddress, o.ClientAddress)
	zipcode := firstNonEmpty(o.BillingZipcode, o.ClientZipcode)

	params := &CheckoutParams{
		ClientDetails: &ClientDetails{
			Name:    o.ClientName,
			Email:   o.ClientEmail,
			Phone:   o.ClientPhone,
			Country: country,
			City:    city,
			Street:  street,
			ZipCode: zipcode,
			TaxId:   o.ClientVAT,
		},
		Total:         floatToCents(o.Total),
		Currency:      o.CurrencyCode,
		OrderId:       o.OrderNumber,
		// The B2B portal is a separate system with its own id space, so OrderNumber can
		// collide with an OpenCart order id. Dedup on the globally-unique order UID instead
		// (stamped into the invoice id_external), while OrderNumber stays as the human
		// reference printed on the invoice.
		ExternalId:    o.OrderUID,
		SuccessUrl:    "https://b2b.internal/success",
		Created:       time.Now(),
		Source:        SourceB2B,
		TaxValue:      floatToCents(o.TotalVAT),
		SubTotal:      floatToCents(o.Subtotal),
		CustomerGroup: DefaultCustomerGroupB2B,
	}

	if o.Shipment > 0 {
		params.Shipping = floatToCents(o.Shipment)
		params.LineItems = append(params.LineItems, ShippingLineItem("", params.Shipping))
	}

	for _, item := range o.Items {
		price := item.Price
		if item.PriceDiscount > 0 {
			price = item.PriceDiscount
		}
		lineItem := &LineItem{
			Name:  item.ProductName,
			Qty:   item.Quantity,
			Price: floatToCents(price),
			Sku:   item.ProductSKU,
		}
		params.LineItems = append(params.LineItems, lineItem)
	}

	return params
}

// firstNonEmpty returns the first value that is not empty or blank.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// floatToCents converts a float64 amount to int64 cents
func floatToCents(amount float64) int64 {
	return int64(math.Round(amount * 100))
}
