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
