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
//   Must be a JSON array string, e.g. `["SW"]`, not a bare string.
//
// VAT MOSS details (vat_moss_details, separate API call — nesting in invoice is silently ignored):
//   Required for OSS invoices. Provides the type of sale classification and
//   two pieces of evidence proving the buyer's country (EU regulation requirement).
//   Added via vat_moss_details/add after invoice creation.
//   Service codes for goods (WSTO): "BA", "BB".
//   Evidence types: "A" (address), "B" (IP/geo), "C" (bank), "D" (SIM),
//   "E" (landline), "F" (other).
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
	Date          string                  `json:"date" bson:"date"`                                     // invoice issue date, format "YYYY-MM-DD"
	Currency      string                  `json:"currency" bson:"currency"`                             // uppercase ISO 4217: "PLN", "EUR"
	TypeOfSale    string                  `json:"type_of_sale,omitempty" bson:"type_of_sale,omitempty"` // JSON array, e.g. '["SW"]' for OSS goods
	Contents      []*ContentLine          `json:"invoicecontents" bson:"invoicecontents"`
	Errors        map[string]ErrorWrapper `json:"errors,omitempty" bson:"errors,omitempty"`
}

// Content represents a single line item in an invoice (invoicecontent).
//
// VAT can be specified in two ways:
//   - VatCode (preferred): references a wFirma vat_code by ID, fetched via vat_codes/find.
//     Required for non-standard rates (EU destination-country rates, WDT, EXP, etc.)
//     because wFirma resets plain "vat" values to the default Polish rate.
//   - Vat (fallback): numeric rate string ("23", "8", "0") or special code ("WDT", "EXP", "NP", "NPUE", "ZW").
//     Used only when vat_code IDs are unavailable.
//
// Non-Polish numeric rates (e.g. "25" for Denmark) require the invoice to have
// type_of_sale set as a JSON array (e.g. '["SW"]') and OSS enabled in wFirma settings.
type Content struct {
	Name    string      `json:"name" bson:"name"`
	Good    *GoodRef    `json:"good,omitempty" bson:"good,omitempty"` // wFirma good reference — links line item to product catalog
	Count   int64       `json:"count" bson:"count"`
	Price   float64     `json:"price" bson:"price"`                           // per-unit price in major currency units (e.g. PLN, not groszy)
	Unit    string      `json:"unit" bson:"unit"`                             // measurement unit, e.g. "szt." (pieces)
	Vat     string      `json:"vat,omitempty" bson:"vat,omitempty"`           // fallback: numeric rate or special code
	VatCode *VatCodeRef `json:"vat_code,omitempty" bson:"vat_code,omitempty"` // preferred: wFirma vat_code reference by ID
}

// VatCodeRef references a wFirma VAT code by its internal ID.
// Used in invoice line items to specify the VAT rate unambiguously.
type VatCodeRef struct {
	ID string `json:"id" bson:"id"`
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

// vat_moss_details fields (used via raw maps in addVatMossDetails, not as a struct):
//
// Service codes (type field):
//   "BA", "BB" — goods (WSTO)
//   "SA"-"SE"  — services
//   "TA"-"TK"  — telecom/broadcasting/electronic
//
// Evidence types (evidence1_type, evidence2_type):
//   "A" — billing/shipping address
//   "B" — IP address / geolocation
//   "C" — bank details
//   "D" — mobile phone country code (SIM)
//   "E" — fixed landline location
//   "F" — other commercially relevant information
