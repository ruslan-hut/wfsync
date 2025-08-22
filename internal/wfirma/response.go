package wfirma

type Response struct {
	Contractors map[string]ContractorWrapper `json:"contractors"`
	Status      Status                       `json:"status"`
}

type ContractorWrapper struct {
	Contractor Contractor `json:"contractor"`
}

type Contractor struct {
	ID        string                  `json:"id"`
	City      string                  `json:"city,omitempty" bson:"city,omitempty"`
	Country   string                  `json:"country,omitempty" bson:"country,omitempty"`
	Email     string                  `json:"email,omitempty" bson:"email,omitempty"`
	Name      string                  `json:"name,omitempty" bson:"name,omitempty"`
	Zip       string                  `json:"zip,omitempty" bson:"zip,omitempty"`
	ErrorsRaw map[string]ErrorWrapper `json:"errors,omitempty" bson:"errors,omitempty"`
}

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

type Status struct {
	Code string `json:"code"`
}

type InvoiceResponse struct {
	Invoices InvoicesWrapper `json:"invoices"`
	Status   Status          `json:"status"`
}

type InvoicesWrapper map[string]InvoiceWrapper

type InvoiceWrapper struct {
	Invoice InvoiceData `json:"invoice"`
}

type InvoiceData struct {
	Id          string                  `json:"id,omitempty" bson:"id"`
	Number      string                  `json:"fullnumber" bson:"number"`
	Type        string                  `json:"type" bson:"type"`
	PriceType   string                  `json:"price_type" bson:"price_type"`
	Total       string                  `json:"total" bson:"total"`
	IdExternal  string                  `json:"id_external" bson:"id_external"`
	Description string                  `json:"description" bson:"description"`
	Date        string                  `json:"date" bson:"date"`
	Currency    string                  `json:"currency" bson:"currency"`
	Errors      map[string]ErrorWrapper `json:"errors,omitempty" bson:"errors,omitempty"`
}
