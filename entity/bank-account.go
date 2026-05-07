package entity

import "time"

// BankAccount mirrors a wFirma company_account record locally.
//
// IsAllowed is a manually toggled flag — only accounts marked true are
// candidates for invoice assignment. Sync from wFirma never overwrites this
// flag, so operators can set it once and trust it across refreshes.
type BankAccount struct {
	ID         string    `bson:"id" json:"id"`                 // wFirma company_account ID (primary key)
	Name       string    `bson:"name" json:"name"`             // wFirma display name (e.g. "konto EUR")
	BankName   string    `bson:"bank_name" json:"bank_name"`
	Number     string    `bson:"number" json:"number"`         // IBAN / account number as stored in wFirma
	Swift      string    `bson:"swift" json:"swift"`
	Currency   string    `bson:"currency" json:"currency"`     // uppercase ISO 4217
	Status     string    `bson:"status" json:"status"`         // wFirma status field, e.g. "accepted"
	Visibility string    `bson:"visibility" json:"visibility"` // wFirma visibility field, e.g. "visible"
	IsAllowed  bool      `bson:"is_allowed" json:"is_allowed"` // operator-set: only true accounts are used on invoices
	SyncedAt   time.Time `bson:"synced_at" json:"synced_at"`
}
