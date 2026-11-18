package entity

import "time"

// BankBalance is one balance the bank reports for an account. A bank usually reports
// several kinds at once — booked and available, closing and intraday — and which ones
// depends on the bank, so they are passed through as reported rather than reduced to one.
type BankBalance struct {
	// Type is the ISO 20022 balance type: CLBD booked, ITAV / CLAV available, etc.
	Type string `json:"type" bson:"type"`
	Name string `json:"name,omitempty" bson:"name,omitempty"`
	// Amount is in minor units and signed: an overdrawn account is negative.
	Amount        int64  `json:"amount" bson:"amount"`
	Currency      string `json:"currency" bson:"currency"`
	ReferenceDate string `json:"reference_date,omitempty" bson:"reference_date,omitempty"`
	LastChange    string `json:"last_change,omitempty" bson:"last_change,omitempty"`
}

// BankAccountBalances groups the balances of one account. Error is set instead when the
// bank refused the request for that account, so one refusal does not hide the others.
type BankAccountBalances struct {
	IBAN     string        `json:"iban"`
	Currency string        `json:"currency"`
	Balances []BankBalance `json:"balances,omitempty"`
	Error    string        `json:"error,omitempty"`
}

// BankBalanceSnapshot is the balance of one account as fetched on one working day. It is
// keyed by IBAN and Date, so a repeated fetch on the same day replaces the earlier one.
type BankBalanceSnapshot struct {
	// Date is the local working day the snapshot belongs to, "YYYY-MM-DD".
	Date      string        `json:"date" bson:"date"`
	IBAN      string        `json:"iban" bson:"iban"`
	Currency  string        `json:"currency" bson:"currency"`
	Balances  []BankBalance `json:"balances" bson:"balances"`
	FetchedAt time.Time     `json:"fetched_at" bson:"fetched_at"`
}
