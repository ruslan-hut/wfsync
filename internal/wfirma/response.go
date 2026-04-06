package wfirma

import (
	"encoding/json"
	"strconv"
)

// wFirma API response structures.
// The API returns objects keyed by index ("0", "1", ...) rather than arrays,
// so we use map[string]...Wrapper to deserialize them.

// Response is the top-level response for contractors/add and contractors/find actions.
type Response struct {
	Contractors map[string]ContractorWrapper `json:"contractors"`
	Status      Status                       `json:"status"`
}

type ContractorWrapper struct {
	Contractor Contractor `json:"contractor"`
}

// Contractor is used both as a request field (inside Invoice, with only ID set)
// and as a response field (with all fields populated).
type Contractor struct {
	ID        string                  `json:"id"`
	City      string                  `json:"city,omitempty" bson:"city,omitempty"`
	Country   string                  `json:"country,omitempty" bson:"country,omitempty"`
	Email     string                  `json:"email,omitempty" bson:"email,omitempty"`
	Name      string                  `json:"name,omitempty" bson:"name,omitempty"`
	Zip       string                  `json:"zip,omitempty" bson:"zip,omitempty"`
	ErrorsRaw ErrorsMap `json:"errors,omitempty" bson:"errors,omitempty"`
}

// ErrorsMap is a map of field-level validation errors returned by the wFirma API.
// The API inconsistently returns this as either a JSON object (when errors exist)
// or a JSON boolean `false` (when no errors). This type handles both.
type ErrorsMap map[string]ErrorWrapper

// UnmarshalJSON accepts a JSON object as a normal map, or `false`/`null` as nil.
func (em *ErrorsMap) UnmarshalJSON(data []byte) error {
	if string(data) == "false" || string(data) == "null" {
		*em = nil
		return nil
	}
	var m map[string]ErrorWrapper
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	*em = m
	return nil
}

// ErrorWrapper / ErrorDetail represent field-level validation errors returned by the API.
type ErrorWrapper struct {
	Error ErrorDetail `json:"error"`
}

type ErrorDetail struct {
	Field   string      `json:"field"`
	Message string      `json:"message"`
	Method  ErrorMethod `json:"method"`
}

type ErrorMethod struct {
	Name       string `json:"name"`
	Parameters string `json:"parameters"`
}

// Status is included in every wFirma API response. Code is "OK" or "ERROR".
type Status struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"` // top-level error message, present when Code == "ERROR"
}

// InvoiceResponse is the top-level response for invoices/add action.
type InvoiceResponse struct {
	Invoices InvoicesWrapper `json:"invoices"`
	Status   Status          `json:"status"`
}

// InvoicesWrapper maps index keys ("0", "1", ...) to invoice wrappers.
type InvoicesWrapper map[string]InvoiceWrapper

type InvoiceWrapper struct {
	Invoice InvoiceData `json:"invoice"`
}

// VatCodesResponse is the top-level response for vat_codes/find action.
type VatCodesResponse struct {
	VatCodes   map[string]VatCodeWrapper `json:"vat_codes"`
	Status     Status                    `json:"status"`
	Parameters FindParameters            `json:"parameters"`
}

type VatCodeWrapper struct {
	VatCode VatCodeData `json:"vat_code"`
}

// VatCodeData represents a single VAT code returned by the wFirma API.
// Polish codes have a non-empty Code (e.g. "23", "WDT") and DeclarationCountry.ID == "0".
// Foreign (OSS) codes have an empty Code, a numeric Rate, and DeclarationCountry.ID > 0.
type VatCodeData struct {
	ID                 string              `json:"id"`
	Code               string              `json:"code"`                // short code, e.g. "23", "WDT"; empty for foreign codes
	Rate               string              `json:"rate"`                // numeric rate, e.g. "19.00", "25.00"
	DeclarationCountry *DeclarationCountry `json:"declaration_country"` // country reference; ID == "0" for Polish codes
}

// DeclarationCountryResponse is the top-level response for declaration_countries/find.
type DeclarationCountryResponse struct {
	Countries  map[string]DeclarationCountryWrapper `json:"declaration_countries"`
	Status     Status                               `json:"status"`
	Parameters FindParameters                       `json:"parameters"`
}

type DeclarationCountryWrapper struct {
	Country DeclarationCountry `json:"declaration_country"`
}

// DeclarationCountry maps a wFirma internal ID to an ISO 3166-1 alpha-2 country code.
// The API inconsistently returns the ID as a string in some contexts and a number in others
// (e.g., as a string in declaration_countries/find but as a number when nested in vat_code).
type DeclarationCountry struct {
	ID   string `json:"id"`
	Code string `json:"code"` // ISO 3166-1 alpha-2, e.g. "SE", "DE"
}

// UnmarshalJSON handles the wFirma API's inconsistent typing of the "id" field
// (sometimes a JSON string, sometimes a JSON number).
func (dc *DeclarationCountry) UnmarshalJSON(data []byte) error {
	// Use a raw struct to avoid infinite recursion.
	var raw struct {
		ID   json.RawMessage `json:"id"`
		Code string          `json:"code"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	dc.Code = raw.Code
	dc.ID = unmarshalFlexString(raw.ID)
	return nil
}

// unmarshalFlexString converts a JSON value (string or number) to a Go string.
// Returns empty string for null or invalid JSON.
func unmarshalFlexString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// Try string first.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// Try number.
	var n json.Number
	if err := json.Unmarshal(raw, &n); err == nil {
		return n.String()
	}
	return ""
}

// InvoiceFindResponse is the top-level response for invoices/find action.
// Separate from InvoiceResponse to avoid breaking the invoices/add flow.
type InvoiceFindResponse struct {
	Invoices   InvoicesWrapper `json:"invoices"`
	Status     Status          `json:"status"`
	Parameters FindParameters  `json:"parameters"`
}

// FindParameters contains pagination metadata returned by find actions.
// The API inconsistently returns these as strings or numbers depending on the endpoint.
type FindParameters struct {
	Limit int `json:"limit"`
	Page  int `json:"page"`
	Total int `json:"total"`
}

// UnmarshalJSON handles the wFirma API returning pagination values as either
// strings ("20") or numbers (20).
func (fp *FindParameters) UnmarshalJSON(data []byte) error {
	var raw struct {
		Limit json.RawMessage `json:"limit"`
		Page  json.RawMessage `json:"page"`
		Total json.RawMessage `json:"total"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	fp.Limit = unmarshalFlexInt(raw.Limit)
	fp.Page = unmarshalFlexInt(raw.Page)
	fp.Total = unmarshalFlexInt(raw.Total)
	return nil
}

// unmarshalFlexInt converts a JSON value (string or number) to an int.
func unmarshalFlexInt(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		return n
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if v, err := strconv.Atoi(s); err == nil {
			return v
		}
	}
	return 0
}

// ContractorErrors captures contractor fields from an invoice response.
// Named "Errors" historically because the invoices/add response only returns errors,
// but invoices/find also returns name and ID.
type ContractorErrors struct {
	ID     string    `json:"id,omitempty"`
	Name   string    `json:"name,omitempty"`
	Errors ErrorsMap `json:"errors,omitempty"`
}

// UnmarshalJSON handles the wFirma API returning contractor.id as either a string or number.
func (ce *ContractorErrors) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID     json.RawMessage `json:"id"`
		Name   string          `json:"name"`
		Errors ErrorsMap       `json:"errors"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	ce.Name = raw.Name
	ce.Errors = raw.Errors
	ce.ID = unmarshalFlexString(raw.ID)
	return nil
}

// InvoiceData represents the invoice object returned by the API.
// Note: Total is a string in responses (the API returns it as a formatted decimal).
type InvoiceData struct {
	Id              string                               `json:"id,omitempty" bson:"id"`
	Number          string                               `json:"fullnumber" bson:"number"` // full formatted invoice number, e.g. "FV 1/01/2025"
	Type            string                               `json:"type" bson:"type"`
	PriceType       string                               `json:"price_type" bson:"price_type"`
	Total           string                               `json:"total" bson:"total"`
	IdExternal      string                               `json:"id_external" bson:"id_external"`
	Description     string                               `json:"description" bson:"description"`
	Date            string                               `json:"date" bson:"date"`
	Currency        string                               `json:"currency" bson:"currency"`
	Contractor      *ContractorErrors                    `json:"contractor,omitempty" bson:"contractor,omitempty"`
	InvoiceContents map[string]InvoiceContentRespWrapper `json:"invoicecontents,omitempty"`
	Errors          ErrorsMap                            `json:"errors,omitempty" bson:"errors,omitempty"`
}

// InvoiceContentRespWrapper wraps a single invoice content item in the API response.
type InvoiceContentRespWrapper struct {
	InvoiceContent InvoiceContentResp `json:"invoicecontent"`
}

// InvoiceContentResp captures the name and errors from an invoice content item in the response.
type InvoiceContentResp struct {
	Name   string    `json:"name"`
	Errors ErrorsMap `json:"errors,omitempty"`
}
