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
// Type of sale (type_of_sale field, JSON-encoded array):
//   "SW" — distance selling of goods (WSTO) under EU OSS
//   "EE" — electronic services under EU OSS
//   Required for invoices with destination-country VAT rates (non-PL EU B2C).
//
// Note: the API computes totals from invoicecontents automatically.
// The "total" field is included for local reference but ignored by the API on create.

// Invoice represents a wFirma invoice payload for the invoices/add API action.
type Invoice struct {
	Id            string                  `json:"id,omitempty" bson:"id"`
	Number        string                  `json:"fullnumber,omitempty" bson:"number"`
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
	Currency      string                  `json:"currency" bson:"currency"`                           // uppercase ISO 4217: "PLN", "EUR"
	TypeOfSale    string                  `json:"type_of_sale,omitempty" bson:"type_of_sale,omitempty"` // JSON array of sale types, e.g. "[\"SW\"]" for OSS goods
	Contents      []*ContentLine          `json:"invoicecontents" bson:"invoicecontents"`
	Errors        map[string]ErrorWrapper `json:"errors,omitempty" bson:"errors,omitempty"`
}

// Content represents a single line item in an invoice (invoicecontent).
// Vat accepts any numeric rate (e.g. "23", "21", "19", "8", "0") and special codes:
//
//	"WDT"  — 0% intra-community goods delivery (EU buyer with VAT number)
//	"EXP"  — 0% export of goods (non-EU buyer)
//	"NP"   — not subject to Polish VAT (non-EU services)
//	"NPUE" — not subject to Polish VAT, EU (EU services, reverse charge)
//	"ZW"   — exempt from VAT
type Content struct {
	Name  string   `json:"name" bson:"name"`
	Good  *GoodRef `json:"good,omitempty" bson:"good,omitempty"` // wFirma good reference — links line item to product catalog
	Count int64    `json:"count" bson:"count"`
	Price float64  `json:"price" bson:"price"` // per-unit price in major currency units (e.g. PLN, not groszy)
	Unit  string   `json:"unit" bson:"unit"`   // measurement unit, e.g. "szt." (pieces)
	Vat   string   `json:"vat" bson:"vat"`     // numeric rate ("23", "21", "19", "0") or special code ("WDT", "EXP", "NP", "NPUE", "ZW")
}

// GoodRef is an entity reference to a wFirma goods catalog item.
// The API expects references as objects: {"id": 12345}, not bare integers.
type GoodRef struct {
	ID int64 `json:"id" bson:"id"`
}

// ContentLine wraps Content for the wFirma API array structure.
// The API expects: "invoicecontents": [{"invoicecontent": {...}}, ...]
type ContentLine struct {
	Content *Content `json:"invoicecontent" bson:"invoicecontent"`
}
