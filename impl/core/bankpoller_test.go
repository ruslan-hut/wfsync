package core

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"wfsync/entity"
	"wfsync/internal/enablebanking"
)

// fakeFetcher stands in for the bank client.
type fakeFetcher struct {
	enabled bool
	txs     []*entity.BankTransaction
	err     error
	calls   []string // IBANs asked for, in order
}

func (f *fakeFetcher) Enabled() bool { return f.enabled }
func (f *fakeFetcher) BankTransactions(_ context.Context, acc enablebanking.Account, _, _ string) ([]*entity.BankTransaction, error) {
	f.calls = append(f.calls, acc.AccountID.IBAN)
	if f.err != nil {
		return nil, f.err
	}
	return f.txs, nil
}

// fakeBankPollDB records what the poller asked it to persist.
type fakeBankPollDB struct {
	session  *entity.BankSession
	polledAt time.Time
	warnedAt time.Time
	expired  string
	stored   []*entity.BankTransaction
}

func (f *fakeBankPollDB) GetActiveBankSession() (*entity.BankSession, error) { return f.session, nil }
func (f *fakeBankPollDB) SaveBankTransactions(txs []*entity.BankTransaction) (int, error) {
	f.stored = append(f.stored, txs...)
	return len(txs), nil
}
func (f *fakeBankPollDB) MarkBankSessionPolled(_ string, at time.Time) error {
	f.polledAt = at
	return nil
}
func (f *fakeBankPollDB) MarkBankSessionExpired(_, reason string) error {
	f.expired = reason
	return nil
}
func (f *fakeBankPollDB) MarkBankSessionWarned(_ string, at time.Time) error {
	f.warnedAt = at
	return nil
}

func testPoller(db BankPollDatabase, warnDays int, ibans ...string) *BankPoller {
	return newTestPoller(&fakeFetcher{enabled: true}, db, warnDays, ibans...)
}

func newTestPoller(f *fakeFetcher, db BankPollDatabase, warnDays int, ibans ...string) *BankPoller {
	p := NewBankPoller(f, slog.New(slog.NewTextHandler(io.Discard, nil)), 30, 7, warnDays, ibans)
	p.SetDatabase(db)
	return p
}

func TestWarnIfExpiring(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name         string
		validUntil   time.Time
		lastWarnedAt time.Time
		wantWarn     bool
	}{
		{
			name:       "far from expiry stays quiet",
			validUntil: now.AddDate(0, 0, 120),
			wantWarn:   false,
		},
		{
			name:       "inside the warning window warns",
			validUntil: now.AddDate(0, 0, 20),
			wantWarn:   true,
		},
		{
			// Re-authorization needs a person at the bank; the reminder repeats daily
			// rather than on every poll.
			name:         "already warned today stays quiet",
			validUntil:   now.AddDate(0, 0, 20),
			lastWarnedAt: now.Add(-2 * time.Hour),
			wantWarn:     false,
		},
		{
			name:         "warned yesterday warns again",
			validUntil:   now.AddDate(0, 0, 20),
			lastWarnedAt: now.Add(-25 * time.Hour),
			wantWarn:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := &fakeBankPollDB{}
			p := testPoller(db, 30)
			p.warnIfExpiring(&entity.BankSession{
				State:        "s1",
				ValidUntil:   tt.validUntil,
				LastWarnedAt: tt.lastWarnedAt,
			}, now)

			warned := !db.warnedAt.IsZero()
			if warned != tt.wantWarn {
				t.Errorf("warned = %v, want %v", warned, tt.wantWarn)
			}
		})
	}
}

func TestPollWithoutSessionIsQuiet(t *testing.T) {
	db := &fakeBankPollDB{session: nil}
	p := testPoller(db, 30)
	p.poll()

	if db.expired != "" {
		t.Errorf("marked expired without any session: %q", db.expired)
	}
	if !db.polledAt.IsZero() {
		t.Error("recorded a poll without any session")
	}
}

// TestPollExpiresLapsedConsent covers the halt path: once the consent is past its date
// the poller must record that and stop, because no retry can renew it — only the account
// holder authorizing again at the bank.
func TestPollExpiresLapsedConsent(t *testing.T) {
	db := &fakeBankPollDB{session: &entity.BankSession{
		State:      "s1",
		SessionID:  "sess",
		Status:     entity.BankSessionActive,
		ValidUntil: time.Now().Add(-time.Hour),
	}}
	p := testPoller(db, 30)
	p.poll()

	if db.expired == "" {
		t.Error("lapsed consent was not marked expired")
	}
	if !db.polledAt.IsZero() {
		t.Error("recorded a successful poll on a lapsed consent")
	}
}

// TestExpireIsReportedOnce keeps a dead consent from alerting on every tick.
func TestExpireIsReportedOnce(t *testing.T) {
	db := &fakeBankPollDB{}
	p := testPoller(db, 30)
	p.expire(&entity.BankSession{State: "s1", Status: entity.BankSessionExpired}, "gone")

	if db.expired != "" {
		t.Error("re-reported an already expired session")
	}
}

func TestBankSessionUsability(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		session entity.BankSession
		want    bool
	}{
		{"active and valid", entity.BankSession{Status: entity.BankSessionActive, SessionID: "s", ValidUntil: now.Add(time.Hour)}, true},
		{"active but lapsed", entity.BankSession{Status: entity.BankSessionActive, SessionID: "s", ValidUntil: now.Add(-time.Hour)}, false},
		{"pending has no session id", entity.BankSession{Status: entity.BankSessionPending, ValidUntil: now.Add(time.Hour)}, false},
		{"revoked", entity.BankSession{Status: entity.BankSessionRevoked, SessionID: "s", ValidUntil: now.Add(time.Hour)}, false},
		// An unparsable expiry from the bank leaves the zero time, which must read as
		// expired rather than as "no limit".
		{"zero expiry reads as expired", entity.BankSession{Status: entity.BankSessionActive, SessionID: "s"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.session.IsUsable(now); got != tt.want {
				t.Errorf("IsUsable = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestPollFiltersByIBAN covers the configured account allowlist: with three accounts on
// one consent, only the listed ones may be fetched — every extra call spends part of the
// bank's daily request budget.
func TestPollFiltersByIBAN(t *testing.T) {
	db := &fakeBankPollDB{session: &entity.BankSession{
		State:      "s1",
		SessionID:  "sess",
		Status:     entity.BankSessionActive,
		ValidUntil: time.Now().AddDate(0, 0, 90),
		Accounts: []entity.BankAccountRef{
			{UID: "u1", IBAN: "PL01", Currency: "PLN"},
			{UID: "u2", IBAN: "PL02", Currency: "EUR"},
			{UID: "u3", IBAN: "PL03", Currency: "USD"},
		},
	}}
	f := &fakeFetcher{enabled: true}
	p := newTestPoller(f, db, 30, "PL01", "PL03")
	p.poll()

	if len(f.calls) != 2 || f.calls[0] != "PL01" || f.calls[1] != "PL03" {
		t.Errorf("fetched %v, want [PL01 PL03]", f.calls)
	}
}

// TestPollFetchesEveryAccountWhenUnfiltered checks the default: an empty IBAN list means
// every account the consent covers.
func TestPollFetchesEveryAccountWhenUnfiltered(t *testing.T) {
	db := &fakeBankPollDB{session: &entity.BankSession{
		State:      "s1",
		SessionID:  "sess",
		Status:     entity.BankSessionActive,
		ValidUntil: time.Now().AddDate(0, 0, 90),
		Accounts: []entity.BankAccountRef{
			{UID: "u1", IBAN: "PL01"},
			{UID: "u2", IBAN: "PL02"},
		},
	}}
	f := &fakeFetcher{enabled: true}
	p := newTestPoller(f, db, 30)
	p.poll()

	if len(f.calls) != 2 {
		t.Errorf("fetched %v accounts, want 2", len(f.calls))
	}
	if db.polledAt.IsZero() {
		t.Error("successful poll was not recorded")
	}
}

// TestPollExpiresOnFetchError covers a consent the bank rejects mid-cycle: the remaining
// accounts must not be attempted, since the same rejection applies to all of them.
func TestPollExpiresOnFetchError(t *testing.T) {
	db := &fakeBankPollDB{session: &entity.BankSession{
		State:      "s1",
		SessionID:  "sess",
		Status:     entity.BankSessionActive,
		ValidUntil: time.Now().AddDate(0, 0, 90),
		Accounts: []entity.BankAccountRef{
			{UID: "u1", IBAN: "PL01"},
			{UID: "u2", IBAN: "PL02"},
		},
	}}
	f := &fakeFetcher{enabled: true, err: enablebanking.ErrSessionExpired}
	p := newTestPoller(f, db, 30)
	p.poll()

	if db.expired == "" {
		t.Error("rejected consent was not marked expired")
	}
	if len(f.calls) != 1 {
		t.Errorf("kept fetching after the consent was rejected: %v", f.calls)
	}
}
