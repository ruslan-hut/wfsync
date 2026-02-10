package wfirma

// wFirma API reference: https://doc.wfirma.pl/
// SDK reference: https://github.com/dbojdo/wFirma
//
// Invoice types (type field):
//   "normal"   — standard VAT invoice (faktura VAT)
//   "proforma" — proforma invoice
//
// Price types (price_type field):
//   "brutto" — prices include VAT (gross)
//   "netto"  — prices exclude VAT (net)
//
// Payment methods (paymentmethod field):
//   "transfer", "cash", "compensation", "cod", "payment_card"
//
// Note: the API computes totals from invoicecontents automatically.
// The "total" field is included for local reference but ignored by the API on create.

// Invoice represents a wFirma invoice payload for the invoices/add API action.
type Invoice struct {
	Id            string                  `json:"id,omitempty" bson:"id"`
	Contractor    *Contractor             `json:"contractor" bson:"contractor"`
	Type          string                  `json:"type" bson:"type"`                   // "normal" or "proforma"
	PriceType     string                  `json:"price_type" bson:"price_type"`       // "brutto" (gross) or "netto" (net)
	PaymentMethod string                  `json:"paymentmethod" bson:"paymentmethod"` // e.g. "transfer", "cash", "payment_card"
	PaymentDate   string                  `json:"paymentdate" bson:"paymentdate"`     // payment due date, format "YYYY-MM-DD"
	DisposalDate  string                  `json:"disposaldate" bson:"disposaldate"`   // date of sale/service, format "YYYY-MM-DD"
	Total         float64                 `json:"total" bson:"total"`                 // informational; API recomputes from contents
	IdExternal    string                  `json:"id_external" bson:"id_external"`
	Description   string                  `json:"description" bson:"description"`
	Date          string                  `json:"date" bson:"date"`         // invoice issue date, format "YYYY-MM-DD"
	Currency      string                  `json:"currency" bson:"currency"` // uppercase ISO 4217: "PLN", "EUR"
	Contents      []*ContentLine          `json:"invoicecontents" bson:"invoicecontents"`
	Errors        map[string]ErrorWrapper `json:"errors,omitempty" bson:"errors,omitempty"`
}

// Content represents a single line item in an invoice (invoicecontent).
// Vat accepts both numeric rates ("23", "8", "5", "0") and special codes:
//
//	"WDT"  — 0% intra-community goods delivery (EU buyer with VAT number)
//	"EXP"  — 0% export of goods (non-EU buyer)
//	"NP"   — not subject to Polish VAT (non-EU services)
//	"NPUE" — not subject to Polish VAT, EU (EU services, reverse charge)
//	"ZW"   — exempt from VAT
type Content struct {
	Name  string  `json:"name" bson:"name"`
	Good  *int64  `json:"good,omitempty" bson:"good,omitempty"` // wFirma good ID — links line item to product catalog
	Count int64   `json:"count" bson:"count"`
	Price float64 `json:"price" bson:"price"` // per-unit price in major currency units (e.g. PLN, not groszy)
	Unit  string  `json:"unit" bson:"unit"`   // measurement unit, e.g. "szt." (pieces)
	Vat   string  `json:"vat" bson:"vat"`     // VAT code: "23", "8", "0", "WDT", "EXP", "NP", "NPUE", "ZW"
}

// ContentLine wraps Content for the wFirma API array structure.
// The API expects: "invoicecontents": [{"invoicecontent": {...}}, ...]
type ContentLine struct {
	Content *Content `json:"invoicecontent" bson:"invoicecontent"`
}
