package entity

import "time"

// Status of a bank authorization session.
const (
	// BankSessionPending is an authorization the account holder has been sent to but
	// has not completed yet. It holds no session id.
	BankSessionPending = "pending"
	// BankSessionActive is an authorized session whose consent is still valid.
	BankSessionActive = "active"
	// BankSessionExpired is a session whose consent has lapsed. Only a fresh
	// authorization by the account holder replaces it; there is no refresh.
	BankSessionExpired = "expired"
	// BankSessionRevoked is a session deliberately taken out of use, typically
	// because a newer one superseded it.
	BankSessionRevoked = "revoked"
)

// BankSession is one authorization of access to the company's bank accounts.
//
// It is stored rather than held in memory because the consent outlives any process:
// it is granted by a person in front of the bank's own login screen, lasts at most
// 180 days, and cannot be renewed — when it lapses that same person has to repeat the
// procedure. Losing the session id on restart would mean asking them to do so early.
//
// A row is created when authorization starts, keyed by State, and completed when the
// bank redirects the account holder back with a code.
type BankSession struct {
	// State ties the bank's redirect back to the request that started it. It is
	// generated per authorization and never reused.
	State string `json:"state" bson:"state"`
	// SessionID is assigned by Enable Banking once the code is exchanged. Empty while
	// the authorization is still pending.
	SessionID string `json:"session_id,omitempty" bson:"session_id,omitempty"`

	Status string `json:"status" bson:"status"`
	ASPSP  string `json:"aspsp,omitempty" bson:"aspsp,omitempty"`

	// ValidUntil is when the consent lapses. The bank caps it at its own maximum,
	// which is 180 days for PKO, regardless of what was requested.
	ValidUntil time.Time `json:"valid_until,omitempty" bson:"valid_until,omitempty"`

	Accounts []BankAccountRef `json:"accounts,omitempty" bson:"accounts,omitempty"`

	CreatedAt     time.Time `json:"created_at" bson:"created_at"`
	AuthorizedAt  time.Time `json:"authorized_at,omitempty" bson:"authorized_at,omitempty"`
	LastPolledAt  time.Time `json:"last_polled_at,omitempty" bson:"last_polled_at,omitempty"`
	LastWarnedAt  time.Time `json:"last_warned_at,omitempty" bson:"last_warned_at,omitempty"`
	FailureReason string    `json:"failure_reason,omitempty" bson:"failure_reason,omitempty"`
}

// BankAccountRef is one account a session grants access to. UID addresses it in API
// calls and is not the IBAN.
type BankAccountRef struct {
	UID      string `json:"uid" bson:"uid"`
	IBAN     string `json:"iban,omitempty" bson:"iban,omitempty"`
	Currency string `json:"currency,omitempty" bson:"currency,omitempty"`
	Name     string `json:"name,omitempty" bson:"name,omitempty"`
	// Usage is "ORGA" for a company account, "PRIV" for a personal one. Worth checking
	// on a live connection: a company account reporting PRIV suggests the holder
	// authorized through the retail interface rather than the business one.
	Usage string `json:"usage,omitempty" bson:"usage,omitempty"`
}

// IsUsable reports whether the session can currently be polled.
func (s *BankSession) IsUsable(now time.Time) bool {
	return s.Status == BankSessionActive && s.SessionID != "" && now.Before(s.ValidUntil)
}

// DaysLeft returns whole days until the consent lapses; negative once it has.
func (s *BankSession) DaysLeft(now time.Time) int {
	return int(s.ValidUntil.Sub(now).Hours() / 24)
}
