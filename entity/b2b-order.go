package entity

import (
	"math"
	"net/http"
	"time"
	"wfsync/lib/validate"
)

const SourceB2B Source = "b2b"

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
	StoreUID        string     `json:"store_uid"`
	Status          string     `json:"status"`
	Total           float64    `json:"total" validate:"required,gt=0"`
	Subtotal        float64    `json:"subtotal"`
	TotalVAT        float64    `json:"total_vat"`
	DiscountPercent float64    `json:"discount_percent"`
	DiscountAmount  float64    `json:"discount_amount"`
	CurrencyCode    string     `json:"currency_code" validate:"required,oneof=PLN EUR"`
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

// ToCheckoutParams converts B2BOrder to CheckoutParams format
func (o *B2BOrder) ToCheckoutParams() *CheckoutParams {
	params := &CheckoutParams{
		ClientDetails: &ClientDetails{
			Name:    o.ClientName,
			Email:   o.ClientEmail,
			Phone:   o.ClientPhone,
			Country: o.ClientCountry,
			City:    o.ClientCity,
			Street:  o.ClientAddress,
			ZipCode: o.ClientZipcode,
			TaxId:   o.ClientVAT,
		},
		Total:      floatToCents(o.Total),
		Currency:   o.CurrencyCode,
		OrderId:    o.OrderNumber,
		SuccessUrl: "https://b2b.internal/success",
		Created:    time.Now(),
		Source:     SourceB2B,
		TaxValue:   floatToCents(o.TotalVAT),
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

// floatToCents converts a float64 amount to int64 cents
func floatToCents(amount float64) int64 {
	return int64(math.Round(amount * 100))
}
