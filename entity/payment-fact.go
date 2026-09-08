package entity

import "time"

// MatchLevel says how a payment was tied to a document, and therefore how far it can be
// trusted without a person looking at it.
const (
	// MatchExact means the document number was found in the payment reference. The
	// number identifies one document, so this is safe to act on automatically.
	MatchExact = "exact"
	// MatchProbable means the payment was tied by amount and counterparty rather than
	// by a stated number. Good enough to propose, never good enough to move an order.
	MatchProbable = "probable"
	// MatchManual means a person made the link.
	MatchManual = "manual"
)

// AmountStatus reports how the paid sum compares with what the document asks for. It is
// deliberately separate from MatchLevel: a payment can identify its document beyond doubt
// and still not settle it in full.
const (
	AmountExact   = "exact"
	AmountPartial = "partial"
	AmountOver    = "over"
	// AmountFX means the payment came in a different currency than the document.
	// Customers routinely pay a EUR proforma in PLN at the day's rate, so the sums
	// cannot be compared without a rate — the document is still identified correctly.
	AmountFX = "fx"
)

// PaymentStatusFact is the settlement state of one order, as derived from the bank.
const (
	PaymentFactUnpaid   = "unpaid"
	PaymentFactPartial  = "partial"
	PaymentFactPaid     = "paid"
	PaymentFactOverpaid = "overpaid"
	PaymentFactRefunded = "refunded"
)

// PaymentFact records that money arrived for a given order, and what it settles.
//
// It is keyed by ExternalRef — the value wFirma stores in id_external, which is the only
// identifier shared by every system involved (see InvoiceListItem). For an OpenCart order
// that is the order id; for the B2B portal it is the order UID.
//
// This is the record the ERP, the B2B portal and the shop read. wFirma is deliberately
// not written back to, so this — not wFirma's own receivables view — is the source of
// truth for whether an order was paid.
type PaymentFact struct {
	ExternalRef string `json:"external_ref" bson:"external_ref"`
	OrderNumber string `json:"order_number,omitempty" bson:"order_number,omitempty"`
	// DocumentNumber and DocumentID name the wFirma document the payment settles.
	DocumentNumber string `json:"document_number,omitempty" bson:"document_number,omitempty"`
	DocumentID     string `json:"document_id,omitempty" bson:"document_id,omitempty"`
	DocumentType   string `json:"document_type,omitempty" bson:"document_type,omitempty"`

	// CurrencyDoc and AmountDue are what the document asks for; CurrencyPaid and
	// AmountPaid are what actually arrived. The two currencies differ on a
	// cross-currency payment, which is common rather than exceptional here.
	CurrencyDoc  string `json:"currency_doc,omitempty" bson:"currency_doc,omitempty"`
	AmountDue    int64  `json:"amount_due,omitempty" bson:"amount_due,omitempty"`
	CurrencyPaid string `json:"currency_paid" bson:"currency_paid"`
	AmountPaid   int64  `json:"amount_paid" bson:"amount_paid"`

	Status       string `json:"status" bson:"status"`
	AmountStatus string `json:"amount_status" bson:"amount_status"`
	MatchLevel   string `json:"match_level" bson:"match_level"`

	PaidAt    time.Time `json:"paid_at" bson:"paid_at"`
	UpdatedAt time.Time `json:"updated_at" bson:"updated_at"`

	// TransactionKeys lists the bank entries that make up AmountPaid, so a disputed
	// total can be traced back to the statement.
	TransactionKeys []string `json:"transaction_keys" bson:"transaction_keys"`

	// PartyName is the payer as the bank reported them, kept for the manual review
	// queue where recognising the customer matters more than the reference.
	PartyName string `json:"party_name,omitempty" bson:"party_name,omitempty"`
}

// Settled reports whether the order can be treated as paid by an automated consumer.
// A probable match is excluded on purpose: it was tied by amount and counterparty, not
// by a stated document number, and acting on it would move an order on a guess.
func (p *PaymentFact) Settled() bool {
	if p.MatchLevel == MatchProbable {
		return false
	}
	return p.Status == PaymentFactPaid || p.Status == PaymentFactOverpaid
}
