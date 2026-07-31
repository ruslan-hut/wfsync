package entity

import "time"

// DateLayout is the date format used throughout the invoice list (YYYY-MM-DD),
// matching both the wFirma API date fields and the endpoint's from/to parameters.
const DateLayout = "2006-01-02"

// wFirma document type values as returned by the API. Kept here (rather than reusing
// the wfirma package constants) so the entity layer stays import-free.
const (
	wfTypeNormal      = "normal"
	wfTypeNormalDraft = "normal_draft"
)

// DocumentType classifies what wFirma document, if any, exists for an order.
// A draft (wersja robocza faktury) is a real registration but not yet a legal
// invoice — it carries no number and must be accepted manually in wFirma — so it
// is reported distinctly from an accepted faktura rather than as "not invoiced".
type DocumentType string

const (
	DocumentNone    DocumentType = ""
	DocumentInvoice DocumentType = "invoice"
	DocumentDraft   DocumentType = "draft"
)

// DocumentTypeFor maps a wFirma invoice type to its report classification.
func DocumentTypeFor(wfType string) DocumentType {
	switch wfType {
	case wfTypeNormal:
		return DocumentInvoice
	case wfTypeNormalDraft:
		return DocumentDraft
	default:
		return DocumentNone
	}
}

// InvoiceListItem represents a single order in the merged invoice list, combining
// what OpenCart, checkout_params (MongoDB) and wFirma each know about it.
//
// Rows are keyed by the order's external reference — the value wFirma stores in
// id_external — because that is the only identifier shared by every source now that
// orders arrive from several systems (see CheckoutParams.ExternalRef). For OpenCart
// that key is the order id; for the B2B portal it is the order UID while OrderId
// stays the human order number.
type InvoiceListItem struct {
	// Date is the primary display date: the order date when known, otherwise the
	// invoice date. OrderDate and InvoiceDate are kept separately because the two
	// mean different things and can fall in different months.
	Date        string `json:"date"`
	OrderDate   string `json:"order_date,omitempty"`
	InvoiceDate string `json:"invoice_date,omitempty"`

	OrderStatus int    `json:"order_status"`
	OrderId     string `json:"order_id"`
	ExternalId  string `json:"external_id,omitempty"`
	Source      Source `json:"source,omitempty"`

	// Invoiced reports whether any faktura (accepted or draft) exists for the order.
	// DocumentType says which. Both are explicit so that a draft — which has no
	// InvoiceNumber — is never misread as an uninvoiced order.
	Invoiced     bool         `json:"invoiced"`
	DocumentType DocumentType `json:"document_type,omitempty"`

	// InvoiceNumber holds the wFirma number, or a comma-separated list when a split
	// order produced several parts sharing one id_external. InvoiceParts counts them.
	InvoiceNumber string `json:"invoice_number"`
	InvoiceId     string `json:"invoice_id,omitempty"`
	InvoiceParts  int    `json:"invoice_parts,omitempty"`

	ContractorName string `json:"contractor_name"`
	IsB2B          bool   `json:"is_b2b"`
	IsStripe       bool   `json:"is_stripe"`
	TotalPLN       int64  `json:"total_pln"`
	TotalEUR       int64  `json:"total_eur"`
	TotalUSD       int64  `json:"total_usd"`
	Currency       string `json:"currency"`
}

// SetTotal records the order amount in the per-currency column, but only for the
// first source that supplies one. Sources are applied in descending trust order
// (OpenCart, then checkout_params, then wFirma), so a non-empty Currency means a
// more authoritative amount is already present.
func (i *InvoiceListItem) SetTotal(currency string, amount int64) {
	if i.Currency != "" || currency == "" || amount == 0 {
		return
	}
	i.Currency = currency
	switch currency {
	case "PLN":
		i.TotalPLN = amount
	case "EUR":
		i.TotalEUR = amount
	case "USD":
		i.TotalUSD = amount
	}
}

// InvoiceListSummary answers the endpoint's headline question — how many orders in
// the range were registered in wFirma — without the caller having to tally rows.
type InvoiceListSummary struct {
	Orders      int `json:"orders"`
	Invoiced    int `json:"invoiced"`
	Drafts      int `json:"drafts"`
	NotInvoiced int `json:"not_invoiced"`
}

// InvoiceListSource records the outcome of one data source. A failed source is
// reported rather than silently dropped: without it an empty list is indistinguishable
// from "nothing was invoiced".
type InvoiceListSource struct {
	Name  string `json:"name"`
	Ok    bool   `json:"ok"`
	Count int    `json:"count"`
	Error string `json:"error,omitempty"`
}

// InvoiceListResult is the payload of GET /v1/wf/list.
type InvoiceListResult struct {
	From    string               `json:"from"`
	To      string               `json:"to"`
	Summary InvoiceListSummary   `json:"summary"`
	Sources []*InvoiceListSource `json:"sources"`
	Items   []*InvoiceListItem   `json:"items"`
}

// AddSource appends the outcome of a data source. A nil err marks it healthy.
func (r *InvoiceListResult) AddSource(name string, count int, err error) {
	src := &InvoiceListSource{Name: name, Count: count, Ok: err == nil}
	if err != nil {
		src.Error = err.Error()
		src.Count = 0
	}
	r.Sources = append(r.Sources, src)
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
