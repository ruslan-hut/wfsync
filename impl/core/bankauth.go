// Package core — bankauth.go drives bank account authorization through Enable Banking.
//
// The flow is deliberately two-legged and involves a person: we ask the provider for
// an authorization URL, the account holder opens it, signs in at the bank and approves
// access with the bank's own mobile app, and the bank then redirects them back to our
// callback with a one-time code. Only exchanging that code yields a session.
//
// Nothing here can be automated away. Consent lasts at most 180 days, has no refresh,
// and each renewal costs the same manual round trip — which is why the session is
// persisted and why a pending authorization is tracked by its state value.
package core

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"time"

	"wfsync/entity"
	"wfsync/internal/enablebanking"
	"wfsync/lib/sl"
)

// bankConsentDays is how long a consent is requested for. The bank lowers it to its
// own maximum when that is shorter — 180 days at PKO.
const bankConsentDays = 180

// BankSessionDatabase defines the persistence the authorization flow needs.
type BankSessionDatabase interface {
	SaveBankSession(session *entity.BankSession) error
	GetBankSessionByState(state string) (*entity.BankSession, error)
	GetActiveBankSession() (*entity.BankSession, error)
	SupersedeBankSessions(keepState string) error
}

// SetEnableBanking injects the Enable Banking client.
func (c *Core) SetEnableBanking(eb *enablebanking.Client) {
	c.eb = eb
}

// SetBankSessionDatabase injects session persistence.
func (c *Core) SetBankSessionDatabase(db BankSessionDatabase) {
	c.bankDb = db
}

// BankStartAuthorization begins account authorization and returns the URL the account
// holder must open. A pending session row is written first, so the callback can be
// tied back to this request and a stray or replayed redirect is rejected.
func (c *Core) BankStartAuthorization(ctx context.Context) (string, error) {
	if c.eb == nil || !c.eb.Enabled() {
		return "", fmt.Errorf("enable banking is not configured")
	}
	if c.bankDb == nil {
		return "", fmt.Errorf("bank session storage is not available")
	}

	state, err := randomState()
	if err != nil {
		return "", err
	}

	validUntil := time.Now().AddDate(0, 0, bankConsentDays)
	auth, err := c.eb.StartAuthorization(ctx, state, validUntil)
	if err != nil {
		return "", fmt.Errorf("start authorization: %w", err)
	}

	session := &entity.BankSession{
		State:     state,
		Status:    entity.BankSessionPending,
		CreatedAt: time.Now(),
	}
	if err := c.bankDb.SaveBankSession(session); err != nil {
		return "", fmt.Errorf("save pending session: %w", err)
	}

	c.log.Info("bank authorization started", slog.String("state", state))
	return auth.URL, nil
}

// BankCompleteAuthorization exchanges the code delivered to the callback for a session
// and stores it.
//
// The state must match a pending authorization we started: the callback is a public
// endpoint the bank drives through the account holder's browser, so an unrecognized or
// already-completed state is refused rather than trusted.
func (c *Core) BankCompleteAuthorization(ctx context.Context, state, code string) (*entity.BankSession, error) {
	if c.eb == nil || !c.eb.Enabled() {
		return nil, fmt.Errorf("enable banking is not configured")
	}
	if c.bankDb == nil {
		return nil, fmt.Errorf("bank session storage is not available")
	}

	pending, err := c.bankDb.GetBankSessionByState(state)
	if err != nil {
		return nil, fmt.Errorf("load pending session: %w", err)
	}
	if pending == nil {
		return nil, fmt.Errorf("unknown authorization state")
	}
	if pending.Status != entity.BankSessionPending {
		return nil, fmt.Errorf("authorization already completed")
	}

	session, err := c.eb.CreateSession(ctx, code)
	if err != nil {
		pending.Status = entity.BankSessionRevoked
		pending.FailureReason = err.Error()
		if saveErr := c.bankDb.SaveBankSession(pending); saveErr != nil {
			c.log.Error("save failed session", sl.Err(saveErr))
		}
		return nil, fmt.Errorf("create session: %w", err)
	}

	pending.SessionID = session.SessionID
	pending.Status = entity.BankSessionActive
	pending.ASPSP = session.ASPSP.Name
	pending.AuthorizedAt = time.Now()
	pending.ValidUntil = parseValidUntil(session.AccessValidUntil)
	pending.Accounts = accountRefs(session.Accounts)

	if err := c.bankDb.SaveBankSession(pending); err != nil {
		return nil, fmt.Errorf("save session: %w", err)
	}
	// Only one consent should be live at a time, otherwise polling would have to pick.
	if err := c.bankDb.SupersedeBankSessions(state); err != nil {
		c.log.Error("supersede previous sessions", sl.Err(err))
	}

	c.log.Info("bank authorization completed",
		slog.String("aspsp", pending.ASPSP),
		slog.Int("accounts", len(pending.Accounts)),
		slog.Time("valid_until", pending.ValidUntil),
		slog.Int("days_left", pending.DaysLeft(time.Now())))

	for _, acc := range pending.Accounts {
		// A company account reported as personal means the holder came in through the
		// retail interface; the statement may then cover the wrong account.
		if acc.Usage != "" && acc.Usage != "ORGA" {
			c.log.Warn("authorized account is not marked as a company account",
				slog.String("iban", acc.IBAN),
				slog.String("usage", acc.Usage))
		}
	}

	return pending, nil
}

// BankSessionStatus reports the currently usable session, or nil when no account has
// been authorized yet.
func (c *Core) BankSessionStatus() (*entity.BankSession, error) {
	if c.bankDb == nil {
		return nil, fmt.Errorf("bank session storage is not available")
	}
	return c.bankDb.GetActiveBankSession()
}

// accountRefs reduces the provider's account payloads to what polling needs.
func accountRefs(accounts []enablebanking.Account) []entity.BankAccountRef {
	refs := make([]entity.BankAccountRef, 0, len(accounts))
	for _, a := range accounts {
		refs = append(refs, entity.BankAccountRef{
			UID:      a.UID,
			IBAN:     a.AccountID.IBAN,
			Currency: a.Currency,
			Name:     a.Name,
			Usage:    a.Usage,
		})
	}
	return refs
}

// parseValidUntil reads the consent expiry. An unparsable value yields the zero time,
// which IsUsable treats as already expired — safer than assuming the consent is live.
func parseValidUntil(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// randomState generates the opaque value tying a callback to the authorization that
// started it.
func randomState() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// BankTransactionDatabase defines read access to stored account movements.
type BankTransactionDatabase interface {
	GetBankTransactionsByDateRange(from, to string) ([]*entity.BankTransaction, error)
}

// SetBankTransactionDatabase injects statement storage.
func (c *Core) SetBankTransactionDatabase(db BankTransactionDatabase) {
	c.bankTxDb = db
}

// BankTransactions returns stored account movements booked within [from, to].
// This is the raw statement feed the ERP consumes; matching payments to documents is a
// separate concern and does not filter this view.
func (c *Core) BankTransactions(_ context.Context, from, to string) ([]*entity.BankTransaction, error) {
	if c.bankTxDb == nil {
		return nil, fmt.Errorf("bank statement storage is not available")
	}
	return c.bankTxDb.GetBankTransactionsByDateRange(from, to)
}
