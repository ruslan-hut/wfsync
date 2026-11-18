package core

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"wfsync/entity"
	"wfsync/internal/enablebanking"
)

type fakeBalanceBank struct {
	fail  map[string]bool // account uid → refuse
	calls map[string]int
}

func (f *fakeBalanceBank) Enabled() bool { return true }
func (f *fakeBalanceBank) Balances(_ context.Context, uid string) ([]enablebanking.Balance, error) {
	f.calls[uid]++
	if f.fail[uid] {
		return nil, errors.New("bank refused")
	}
	return []enablebanking.Balance{{BalanceType: "ITBD", BalanceAmount: enablebanking.Amount{Currency: "PLN", Amount: "10.00"}}}, nil
}

type fakeBalanceDB struct {
	session *entity.BankSession
	saved   map[string]*entity.BankBalanceSnapshot // iban|date
}

func (f *fakeBalanceDB) GetActiveBankSession() (*entity.BankSession, error) { return f.session, nil }
func (f *fakeBalanceDB) SaveBankBalance(s *entity.BankBalanceSnapshot) error {
	f.saved[s.IBAN+"|"+s.Date] = s
	return nil
}
func (f *fakeBalanceDB) GetBankBalancesByDateRange(from, to string) ([]*entity.BankBalanceSnapshot, error) {
	var out []*entity.BankBalanceSnapshot
	for _, s := range f.saved {
		if s.Date >= from && s.Date <= to {
			out = append(out, s)
		}
	}
	return out, nil
}

func newTestSnapshotter(t *testing.T, failUID string) (*BalanceSnapshotter, *fakeBalanceBank, *fakeBalanceDB) {
	t.Helper()
	bank := &fakeBalanceBank{fail: map[string]bool{failUID: true}, calls: map[string]int{}}
	db := &fakeBalanceDB{
		session: &entity.BankSession{
			Status: entity.BankSessionActive, SessionID: "s", ValidUntil: time.Date(2027, 5, 17, 0, 0, 0, 0, time.UTC),
			Accounts: []entity.BankAccountRef{{UID: "u1", IBAN: "PL01", Currency: "PLN"}, {UID: "u2", IBAN: "PL02", Currency: "EUR"}},
		},
		saved: map[string]*entity.BankBalanceSnapshot{},
	}
	loc, err := time.LoadLocation("Europe/Warsaw")
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewBalanceSnapshotter(bank, db, slog.New(slog.NewTextHandler(io.Discard, nil)), loc, "07:00", 30, 3)
	if err != nil {
		t.Fatal(err)
	}
	return s, bank, db
}

// warsaw returns a moment in local Warsaw time; 2026-11-18 is a Wednesday.
func warsaw(day, hour, min int) time.Time {
	loc, _ := time.LoadLocation("Europe/Warsaw")
	return time.Date(2026, 11, day, hour, min, 0, 0, loc)
}

func TestBalanceSnapshotSchedule(t *testing.T) {
	tests := []struct {
		name string
		at   time.Time
		want int // snapshots saved
	}{
		{"before the set time", warsaw(18, 6, 59), 0},
		{"at the set time", warsaw(18, 7, 0), 2},
		{"saturday", warsaw(21, 9, 0), 0},
		{"sunday", warsaw(22, 9, 0), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _, db := newTestSnapshotter(t, "")
			s.run(tt.at)
			if len(db.saved) != tt.want {
				t.Errorf("saved %d, want %d", len(db.saved), tt.want)
			}
		})
	}
}

// TestBalanceSnapshotOncePerDay guards the request budget: a completed day is not fetched
// again, even across a restart that loses the in-memory state.
func TestBalanceSnapshotOncePerDay(t *testing.T) {
	s, bank, db := newTestSnapshotter(t, "")
	s.run(warsaw(18, 7, 0))
	s.run(warsaw(18, 8, 0))
	if bank.calls["u1"] != 1 {
		t.Errorf("calls after completion = %d, want 1", bank.calls["u1"])
	}

	restarted, _, _ := newTestSnapshotter(t, "")
	restarted.eb, restarted.db = bank, db
	restarted.run(warsaw(18, 9, 0))
	if bank.calls["u1"] != 1 {
		t.Errorf("calls after restart = %d, want 1", bank.calls["u1"])
	}

	s.run(warsaw(19, 7, 0))
	if bank.calls["u1"] != 2 || db.saved["PL01|2026-11-19"] == nil {
		t.Errorf("next working day not fetched: calls=%d", bank.calls["u1"])
	}
}

// TestBalanceSnapshotRetry retries only the failed account, waits the retry interval,
// and stops after the configured number of attempts.
func TestBalanceSnapshotRetry(t *testing.T) {
	s, bank, db := newTestSnapshotter(t, "u2")

	s.run(warsaw(18, 7, 0))
	s.run(warsaw(18, 7, 10)) // within the retry interval
	if bank.calls["u2"] != 1 {
		t.Fatalf("u2 calls = %d, want 1 before the retry interval", bank.calls["u2"])
	}
	for _, m := range []int{30, 60, 90, 120} {
		s.run(warsaw(18, 7, m%60).Add(time.Duration(m/60) * time.Hour))
	}
	if bank.calls["u1"] != 1 {
		t.Errorf("u1 calls = %d, want 1 — a saved account was fetched again", bank.calls["u1"])
	}
	if bank.calls["u2"] != 3 {
		t.Errorf("u2 calls = %d, want 3 (max attempts)", bank.calls["u2"])
	}
	if db.saved["PL02|2026-11-18"] != nil {
		t.Error("failed account has a snapshot")
	}
}

func TestBalanceSnapshotNoSession(t *testing.T) {
	s, bank, db := newTestSnapshotter(t, "")
	db.session = nil
	s.run(warsaw(18, 7, 0))
	if len(bank.calls) != 0 {
		t.Errorf("bank called without a session: %v", bank.calls)
	}
}
