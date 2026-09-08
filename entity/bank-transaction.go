package entity

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"
)

// Direction of a bank transaction, following the ISO 20022 credit/debit indicator.
const (
	DirectionCredit = "CRDT" // money in
	DirectionDebit  = "DBIT" // money out
)

// Match state of a bank transaction. Stored on the statement row so the review queue is
// a plain query rather than a diff against payment facts.
const (
	BankMatchUnmatched = "unmatched"
	BankMatchMatched   = "matched"
	// BankMatchIgnored marks an entry that is not a customer payment at all — a Stripe
	// payout, a fee, a tax transfer — so it never reaches the review queue.
	BankMatchIgnored = "ignored"
)

// Source of a bank transaction. The matcher is source-agnostic: a statement fetched
// over PSD2 and one imported from a bank's own export must reach it in the same shape.
const (
	BankSourceEnableBanking = "enablebanking"
	BankSourcePKOExport     = "pko_export"
)

// BankTransaction is one movement on a company account, normalized so that the payment
// matcher never has to know where it came from.
//
// Two normalizations matter. Amount is always positive with the direction held
// separately, because Enable Banking reports an unsigned amount plus an indicator
// while a bank's CSV export signs the amount instead. Remittance is a single string,
// because Enable Banking splits the payment reference into lines while an export keeps
// one field — and PKO's export additionally breaks words at fixed-width boundaries,
// which is why matching runs against RemittanceKey rather than the raw text.
type BankTransaction struct {
	// Key is the idempotency key, stable across re-reads of the same entry. It is a
	// hash rather than the bank's own reference because that reference is not unique:
	// in a PKO export the fee charged for a transfer carries the same transaction
	// identifier as the transfer itself.
	Key string `json:"key" bson:"key"`

	AccountIBAN string `json:"account_iban" bson:"account_iban"`
	Direction   string `json:"direction" bson:"direction"`
	// Amount is in minor units and always positive.
	Amount   int64  `json:"amount" bson:"amount"`
	Currency string `json:"currency" bson:"currency"`

	BookingDate string `json:"booking_date" bson:"booking_date"`
	ValueDate   string `json:"value_date,omitempty" bson:"value_date,omitempty"`

	// Remittance is the payment reference as the bank reported it.
	Remittance string `json:"remittance" bson:"remittance"`
	// RemittanceKey is Remittance with all whitespace removed. Document numbers are
	// searched here so that a reference split mid-token ("PROF 74 5/2026") still
	// matches, whatever the source did to the text.
	RemittanceKey string `json:"remittance_key" bson:"remittance_key"`

	// PartyName and PartyIBAN describe the other side of the transaction: the payer
	// for a credit, the payee for a debit.
	PartyName string `json:"party_name,omitempty" bson:"party_name,omitempty"`
	PartyIBAN string `json:"party_iban,omitempty" bson:"party_iban,omitempty"`

	// EntryRef is the bank's own reference, kept for support enquiries. It carries no
	// uniqueness guarantee — see Key.
	EntryRef string `json:"entry_ref,omitempty" bson:"entry_ref,omitempty"`
	// BankCode is the bank's classification of the entry, used to tell an ordinary
	// transfer from a fee, a card payment or a tax transfer.
	BankCode string `json:"bank_code,omitempty" bson:"bank_code,omitempty"`

	Source string `json:"source" bson:"source"`

	// MatchStatus and MatchedRef are written by the matcher, never by the poller: the
	// sliding window re-delivers stored entries on every run, and re-writing the whole
	// row would erase the match.
	MatchStatus string `json:"match_status,omitempty" bson:"match_status,omitempty"`
	MatchedRef  string `json:"matched_ref,omitempty" bson:"matched_ref,omitempty"`
	// Raw is the source record as received, so a disputed match can be re-examined
	// without going back to the bank.
	Raw string `json:"-" bson:"raw,omitempty"`
}

// IsCredit reports whether the transaction is money coming in — the only direction
// that can settle an issued document.
func (t *BankTransaction) IsCredit() bool { return t.Direction == DirectionCredit }

var whitespace = regexp.MustCompile(`\s+`)

// NormalizeRemittance strips every whitespace character and upper-cases the result,
// producing the form document numbers are searched in.
//
// Banks wrap long fields at fixed widths and join the pieces with spaces, so a word —
// including a document number — can be broken at any position. Removing whitespace
// entirely is robust to wherever the break landed, which anchoring on a column is not.
func NormalizeRemittance(s string) string {
	return strings.ToUpper(whitespace.ReplaceAllString(s, ""))
}

// SetRemittance stores the payment reference and derives its normalized form.
func (t *BankTransaction) SetRemittance(s string) {
	t.Remittance = strings.TrimSpace(s)
	t.RemittanceKey = NormalizeRemittance(t.Remittance)
}

// BuildKey derives the idempotency key from the fields that together identify an
// entry, and assigns it. It must be called after the rest of the struct is populated.
//
// The bank reference alone is not enough: PKO reuses one transaction identifier for a
// transfer and the fee charged for it, so keying on it would silently collapse the two
// into a single row and send a wrong statement to the ERP. Amount, direction and
// remittance are what separate them.
func (t *BankTransaction) BuildKey() {
	h := sha256.New()
	for _, part := range []string{
		t.Source,
		t.AccountIBAN,
		t.BookingDate,
		t.Direction,
		t.Currency,
		t.EntryRef,
		t.PartyIBAN,
		t.RemittanceKey,
	} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	h.Write([]byte(strconv.FormatInt(t.Amount, 10)))
	t.Key = hex.EncodeToString(h.Sum(nil))
}

// AssignKeys builds the idempotency keys for one fetched batch, in the order the bank
// returned it.
//
// It exists because a statement can legitimately contain two entries that are
// identical in every field the bank reports — same reference, amount, date, payer and
// description. Keying each one on its content alone would fold them into a single row
// and lose the second payment. Entries that repeat are therefore numbered by their
// position among identical siblings.
//
// The numbering is reproducible across polls: a re-read of the same window returns the
// same entries in the same order, and a newly booked identical entry is appended as
// the next occurrence rather than renumbering the earlier ones. Callers must pass a
// whole window, not a partial page, for that to hold.
func AssignKeys(txs []*BankTransaction) {
	seen := make(map[string]int, len(txs))
	for _, t := range txs {
		t.BuildKey()
		base := t.Key
		n := seen[base]
		seen[base] = n + 1
		if n > 0 {
			h := sha256.New()
			h.Write([]byte(base))
			h.Write([]byte{0})
			h.Write([]byte(strconv.Itoa(n)))
			t.Key = hex.EncodeToString(h.Sum(nil))
		}
	}
}
