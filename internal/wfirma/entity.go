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
// OSS (One-Stop Shop) invoices:
//   For EU B2C sales to non-PL countries, the correct foreign vat_code ID must be used
//   on line items. These IDs are resolved via declaration_countries/find → vat_codes/find.
//   Example: Sweden 25% → declaration_country 205 → vat_code 688.
//   Additionally, vat_moss_details must be provided as a singular nested object inside the
//   invoice — the API validates that type, evidence1_type, and evidence2_type are non-empty.
//
// Note: the API computes totals from invoicecontents automatically.
// The "total" field is included for local reference but ignored by the API on create.

// Invoice represents a wFirma invoice payload for the invoices/add API action.
type Invoice struct {
	Id             string                  `json:"id,omitempty" bson:"id"`
	Number         string                  `json:"fullnumber,omitempty" bson:"number"`
	Contractor     *Contractor             `json:"contractor" bson:"contractor"`
	Type           string                  `json:"type" bson:"type"`                   // "normal" or "proforma"
	PriceType      string                  `json:"price_type" bson:"price_type"`       // "brutto" (gross) or "netto" (net)
	PaymentMethod  string                  `json:"paymentmethod" bson:"paymentmethod"` // e.g. "transfer", "cash", "payment_card"
	PaymentDate    string                  `json:"paymentdate" bson:"paymentdate"`     // payment due date, format "YYYY-MM-DD"
	DisposalDate   string                  `json:"disposaldate" bson:"disposaldate"`   // date of sale/service, format "YYYY-MM-DD"
	Total          float64                 `json:"total" bson:"total"`                 // informational; API recomputes from contents
	IdExternal     string                  `json:"id_external" bson:"id_external"`
	Description    string                  `json:"description" bson:"description"`
	Date           string                  `json:"date" bson:"date"`                                     // invoice issue date, format "YYYY-MM-DD"
	Currency       string                  `json:"currency" bson:"currency"`                             // uppercase ISO 4217: "PLN", "EUR"
	TypeOfSale     string                  `json:"type_of_sale,omitempty" bson:"type_of_sale,omitempty"` // JSON array, e.g. '["SW"]' for OSS goods
	Contents       []*ContentLine          `json:"invoicecontents" bson:"invoicecontents"`
	VatMossDetails *VatMossDetailWrapper   `json:"vat_moss_details,omitempty" bson:"vat_moss_details,omitempty"`
	Errors         map[string]ErrorWrapper `json:"errors,omitempty" bson:"errors,omitempty"`
}

// VatMossDetailWrapper wraps a VatMossDetail for the wFirma API singular relation.
// The API expects: "vat_moss_details": {"vat_moss_detail": {...}}
type VatMossDetailWrapper struct {
	Detail *VatMossDetail `json:"vat_moss_detail" bson:"vat_moss_detail"`
}

// VatMossDetail represents OSS evidence attached to an invoice.
// Required when using foreign vat_code IDs — the API validates all fields are non-empty.
type VatMossDetail struct {
	Type                 string `json:"type" bson:"type"`                                   // "BA"/"BB" (goods), "SA"-"SE" (services)
	Evidence1Type        string `json:"evidence1_type" bson:"evidence1_type"`               // "A" (address), "B" (IP), "C" (bank), "D" (SIM), "E" (landline), "F" (other)
	Evidence1Description string `json:"evidence1_description" bson:"evidence1_description"` // e.g. customer's address
	Evidence2Type        string `json:"evidence2_type" bson:"evidence2_type"`               // same codes as above
	Evidence2Description string `json:"evidence2_description" bson:"evidence2_description"` // e.g. delivery country
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

