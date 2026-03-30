package entity

import "time"

// InvoiceListItem represents a single record in the merged invoice list.
// Combines data from WFirma, OpenCart, and MongoDB to provide a unified view.
type InvoiceListItem struct {
	Date           string `json:"date"`
	OrderStatus    int    `json:"order_status"`
	OrderId        string `json:"order_id"`
	InvoiceNumber  string `json:"invoice_number"`
	ContractorName string `json:"contractor_name"`
	IsB2B          bool   `json:"is_b2b"`
	IsStripe       bool   `json:"is_stripe"`
	TotalPLN       int64  `json:"total_pln"`
	TotalEUR       int64  `json:"total_eur"`
	Currency       string `json:"currency"`
}

// OrderSummary is a lightweight order representation for the invoice list.
// Unlike CheckoutParams, it skips line items, shipping, and tax details.
type OrderSummary struct {
	OrderId       string
	DateAdded     time.Time
	ClientName    string
	Email         string
	Currency      string
	CurrencyValue float64
	Total         int64
	InvoiceId     string
	CustomerGroup int
	OrderStatus   int
}
