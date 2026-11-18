package entity

// BankBalance is one balance the bank reports for an account. A bank usually reports
// several kinds at once — booked and available, closing and intraday — and which ones
// depends on the bank, so they are passed through as reported rather than reduced to one.
type BankBalance struct {
	// Type is the ISO 20022 balance type: CLBD booked, ITAV / CLAV available, etc.
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
	// Amount is in minor units and signed: an overdrawn account is negative.
	Amount        int64  `json:"amount"`
	Currency      string `json:"currency"`
	ReferenceDate string `json:"reference_date,omitempty"`
	LastChange    string `json:"last_change,omitempty"`
}

// BankAccountBalances groups the balances of one account. Error is set instead when the
// bank refused the request for that account, so one refusal does not hide the others.
type BankAccountBalances struct {
	IBAN     string        `json:"iban"`
	Currency string        `json:"currency"`
	Balances []BankBalance `json:"balances,omitempty"`
	Error    string        `json:"error,omitempty"`
}
