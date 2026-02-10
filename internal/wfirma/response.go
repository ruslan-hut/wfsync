package wfirma

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
	ErrorsRaw map[string]ErrorWrapper `json:"errors,omitempty" bson:"errors,omitempty"`
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
	Code string `json:"code"`
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

// InvoiceData represents the invoice object returned by the API.
// Note: Total is a string in responses (the API returns it as a formatted decimal).
type InvoiceData struct {
	Id          string                  `json:"id,omitempty" bson:"id"`
	Number      string                  `json:"fullnumber" bson:"number"` // full formatted invoice number, e.g. "FV 1/01/2025"
	Type        string                  `json:"type" bson:"type"`
	PriceType   string                  `json:"price_type" bson:"price_type"`
	Total       string                  `json:"total" bson:"total"`
	IdExternal  string                  `json:"id_external" bson:"id_external"`
	Description string                  `json:"description" bson:"description"`
	Date        string                  `json:"date" bson:"date"`
	Currency    string                  `json:"currency" bson:"currency"`
	Errors      map[string]ErrorWrapper `json:"errors,omitempty" bson:"errors,omitempty"`
}
