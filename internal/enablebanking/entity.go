package enablebanking

// Payloads of the Enable Banking API. Field names follow the API verbatim; the
// conversion into the source-agnostic entity.BankTransaction lives in transaction.go.

// Application describes the registered API application, returned by GET /application.
// Fetching it is the cheapest way to verify that JWT signing and the key pair work.
type Application struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	KID          string   `json:"kid"`
	Environment  string   `json:"environment"`
	RedirectURLs []string `json:"redirect_urls"`
	Active       bool     `json:"active"`
	Countries    []string `json:"countries"`
	Services     []string `json:"services"`
}

// ASPSP identifies a bank connector. Name plus Country address a connector uniquely.
type ASPSP struct {
	Name    string `json:"name"`
	Country string `json:"country"`
	Logo    string `json:"logo,omitempty"`
	// PSUTypes lists the supported access types, "business" and/or "personal".
	PSUTypes []string `json:"psu_types,omitempty"`
	// MaximumConsentValidity is the longest consent lifetime in seconds. PKO and
	// Bank Millennium both report 15552000 (180 days).
	MaximumConsentValidity int  `json:"maximum_consent_validity,omitempty"`
	Beta                   bool `json:"beta,omitempty"`
}

// ASPSPList is the GET /aspsps response.
type ASPSPList struct {
	ASPSPs []ASPSP `json:"aspsps"`
}

// AuthRequest starts an authorization. The account holder opens the returned URL,
// signs in at the bank and approves access; the bank then redirects to RedirectURL.
type AuthRequest struct {
	Access                AuthAccess `json:"access"`
	ASPSP                 ASPSPRef   `json:"aspsp"`
	State                 string     `json:"state"`
	RedirectURL           string     `json:"redirect_url"`
	PSUType               string     `json:"psu_type"`
	Language              string     `json:"language,omitempty"`
	CredentialsAutoSubmit bool       `json:"credentials_auto_submit,omitempty"`
}

// AuthAccess declares how long the consent should last. ValidUntil is capped by the
// bank's maximum_consent_validity, and cannot be extended later — when it lapses the
// account holder must authorize again in person.
type AuthAccess struct {
	ValidUntil string `json:"valid_until"`
}

// ASPSPRef selects the connector for an authorization request.
type ASPSPRef struct {
	Name    string `json:"name"`
	Country string `json:"country"`
}

// AuthResponse carries the URL to send the account holder to.
type AuthResponse struct {
	URL             string `json:"url"`
	AuthorizationID string `json:"authorization_id,omitempty"`
	PSUIDHash       string `json:"psu_id_hash,omitempty"`
}

// SessionRequest exchanges the code from the redirect callback for a session.
type SessionRequest struct {
	Code string `json:"code"`
}

// Session is an authorized connection to the bank, valid until AccessValidUntil.
type Session struct {
	SessionID string    `json:"session_id"`
	Accounts  []Account `json:"accounts"`
	// AccessValidUntil is when the consent lapses (RFC 3339). Re-authorization by the
	// account holder is the only way past it; there is no refresh.
	AccessValidUntil string `json:"access_valid_until"`
	ASPSP            ASPSP  `json:"aspsp,omitempty"`
	PSUType          string `json:"psu_type,omitempty"`
	Status           string `json:"status,omitempty"`
}

// Account describes one bank account exposed by a session.
type Account struct {
	// UID addresses the account in later calls; it is not the IBAN.
	UID             string       `json:"uid"`
	Identification  string       `json:"identification_hash,omitempty"`
	AccountID       AccountID    `json:"account_id"`
	AllAccountIDs   []AccountRef `json:"all_account_ids,omitempty"`
	Currency        string       `json:"currency"`
	Name            string       `json:"name,omitempty"`
	Product         string       `json:"product,omitempty"`
	CashAccountType string       `json:"cash_account_type,omitempty"`
	// Usage is "ORGA" for a company account and "PRIV" for a personal one.
	Usage      string `json:"usage,omitempty"`
	ResourceID string `json:"resource_id,omitempty"`
}

// AccountID holds the account's primary identifiers.
type AccountID struct {
	IBAN  string      `json:"iban,omitempty"`
	Other *AccountRef `json:"other,omitempty"`
}

// AccountRef is one identifier of an account under a given scheme (IBAN, BBAN, ...).
type AccountRef struct {
	SchemeName     string `json:"scheme_name,omitempty"`
	Identification string `json:"identification,omitempty"`
	Issuer         string `json:"issuer,omitempty"`
}

// Amount is a monetary value. Amount is a decimal string and is always positive —
// direction is carried by Transaction.CreditDebitIndicator, never by a sign.
type Amount struct {
	Currency string `json:"currency"`
	Amount   string `json:"amount"`
}

// Party is a counterparty (debtor or creditor) of a transaction.
type Party struct {
	Name           string `json:"name,omitempty"`
	PostalAddress  any    `json:"postal_address,omitempty"`
	OrganisationID any    `json:"organisation_id,omitempty"`
	PrivateID      any    `json:"private_id,omitempty"`
}

// PartyAccount is a counterparty's account.
type PartyAccount struct {
	IBAN  string      `json:"iban,omitempty"`
	Other *AccountRef `json:"other,omitempty"`
}

// BankTransactionCode is the bank's own classification of the entry.
type BankTransactionCode struct {
	Code        string `json:"code,omitempty"`
	SubCode     string `json:"sub_code,omitempty"`
	Description string `json:"description,omitempty"`
}

// Transaction is one entry of an account statement.
//
// Two shapes differ from a bank's own CSV export and are normalized away in
// transaction.go: Amount is unsigned with the direction in CreditDebitIndicator, and
// RemittanceInformation is a list of lines rather than a single string.
type Transaction struct {
	EntryReference      string              `json:"entry_reference,omitempty"`
	TransactionAmount   Amount              `json:"transaction_amount"`
	Creditor            *Party              `json:"creditor,omitempty"`
	CreditorAccount     *PartyAccount       `json:"creditor_account,omitempty"`
	Debtor              *Party              `json:"debtor,omitempty"`
	DebtorAccount       *PartyAccount       `json:"debtor_account,omitempty"`
	BankTransactionCode BankTransactionCode `json:"bank_transaction_code,omitempty"`
	// CreditDebitIndicator is "CRDT" for money in and "DBIT" for money out.
	CreditDebitIndicator string `json:"credit_debit_indicator"`
	// Status is "BOOK" for a settled entry and "PDNG" for a pending one. Only booked
	// entries are durable enough to reconcile against.
	Status                  string   `json:"status"`
	BookingDate             string   `json:"booking_date,omitempty"`
	ValueDate               string   `json:"value_date,omitempty"`
	TransactionDate         string   `json:"transaction_date,omitempty"`
	BalanceAfterTransaction *Amount  `json:"balance_after_transaction,omitempty"`
	ReferenceNumber         string   `json:"reference_number,omitempty"`
	RemittanceInformation   []string `json:"remittance_information,omitempty"`
	MerchantCategoryCode    string   `json:"merchant_category_code,omitempty"`
}

// TransactionList is one page of GET /accounts/{uid}/transactions. A non-empty
// ContinuationKey means more pages follow.
type TransactionList struct {
	Transactions    []Transaction `json:"transactions"`
	ContinuationKey string        `json:"continuation_key,omitempty"`
}

// Balance is one balance figure reported for an account.
type Balance struct {
	Name           string `json:"name,omitempty"`
	BalanceAmount  Amount `json:"balance_amount"`
	BalanceType    string `json:"balance_type,omitempty"`
	LastChangeDate string `json:"last_change_date_time,omitempty"`
	ReferenceDate  string `json:"reference_date,omitempty"`
}

// BalanceList is the GET /accounts/{uid}/balances response.
type BalanceList struct {
	Balances []Balance `json:"balances"`
}

// APIError is the error body returned by the Enable Banking API.
type APIError struct {
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
	Code    string `json:"code,omitempty"`
}
