// Package core — bankbalances.go takes one balance snapshot per account per working day,
// so the ERP can read balances from this service instead of asking the bank.
//
// Every request to the bank counts against the limit PSD2 allows without the account
// holder present, so balances are fetched once, at a fixed local time, and served from
// storage. A failed account is retried on its own a bounded number of times; accounts
// already saved for the day are never fetched again. Weekends are skipped — nothing is
// booked on them. Public holidays are not known here and are fetched like any weekday.
//
// Following the Start/Stop pattern of reconciler.go.
package core

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"wfsync/entity"
	"wfsync/internal/enablebanking"
	"wfsync/lib/sl"
)

// balanceTick is how often the snapshotter checks whether a fetch is due. The fetch
// itself happens at most once per retry interval; the tick only bounds how late it starts.
const balanceTick = time.Minute

// BalanceFetcher is the bank side of the snapshot.
type BalanceFetcher interface {
	Enabled() bool
	Balances(ctx context.Context, accountUID string) ([]enablebanking.Balance, error)
}

// BankBalanceDatabase defines the persistence the snapshotter needs.
type BankBalanceDatabase interface {
	GetActiveBankSession() (*entity.BankSession, error)
	SaveBankBalance(s *entity.BankBalanceSnapshot) error
	GetBankBalancesByDateRange(from, to string) ([]*entity.BankBalanceSnapshot, error)
}

// BalanceSnapshotter fetches account balances once per working day.
type BalanceSnapshotter struct {
	eb          BalanceFetcher
	db          BankBalanceDatabase
	log         *slog.Logger
	loc         *time.Location
	at          time.Duration // offset from local midnight
	retry       time.Duration
	maxAttempts int

	// Per-day state, reset when the local date changes. Losing it on restart is
	// harmless: what was saved is read back from storage, so only the attempt count
	// starts over.
	day      string
	attempts int
	nextTry  time.Time
	complete bool

	done    chan struct{}
	stopped chan struct{}
}

// NewBalanceSnapshotter creates a snapshotter that fetches at local time at ("HH:MM").
func NewBalanceSnapshotter(eb BalanceFetcher, db BankBalanceDatabase, log *slog.Logger, loc *time.Location, at string, retryMin, maxAttempts int) (*BalanceSnapshotter, error) {
	t, err := time.Parse("15:04", at)
	if err != nil {
		return nil, fmt.Errorf("balance time %q: %w", at, err)
	}
	if loc == nil {
		loc = time.UTC
	}
	if retryMin <= 0 {
		retryMin = 30
	}
	if maxAttempts <= 0 {
		maxAttempts = 6
	}
	return &BalanceSnapshotter{
		eb:          eb,
		db:          db,
		log:         log.With(sl.Module("bankbalances")),
		loc:         loc,
		at:          time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute,
		retry:       time.Duration(retryMin) * time.Minute,
		maxAttempts: maxAttempts,
	}, nil
}

// Start launches the background goroutine.
func (s *BalanceSnapshotter) Start() {
	s.done = make(chan struct{})
	s.stopped = make(chan struct{})
	go func() {
		defer close(s.stopped)
		s.run(time.Now())
		ticker := time.NewTicker(balanceTick)
		defer ticker.Stop()
		for {
			select {
			case <-s.done:
				return
			case now := <-ticker.C:
				s.run(now)
			}
		}
	}()
}

// Stop signals the goroutine to exit and waits for it.
func (s *BalanceSnapshotter) Stop() {
	if s.done != nil {
		close(s.done)
		<-s.stopped
	}
}

// run fetches the day's missing snapshots when one is due.
func (s *BalanceSnapshotter) run(now time.Time) {
	if s.db == nil || s.eb == nil || !s.eb.Enabled() {
		return
	}
	local := now.In(s.loc)
	if wd := local.Weekday(); wd == time.Saturday || wd == time.Sunday {
		return
	}
	midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, s.loc)
	if local.Before(midnight.Add(s.at)) {
		return
	}

	date := local.Format(time.DateOnly)
	if date != s.day {
		s.day, s.attempts, s.nextTry, s.complete = date, 0, time.Time{}, false
	}
	if s.complete || s.attempts >= s.maxAttempts || now.Before(s.nextTry) {
		return
	}
	s.nextTry = now.Add(s.retry)

	session, err := s.db.GetActiveBankSession()
	if err != nil {
		s.log.Error("get active bank session", sl.Err(err))
		return
	}
	// No usable consent is the poller's to report; retrying here would only repeat it.
	if session == nil || !session.IsUsable(now) {
		return
	}

	saved, err := s.db.GetBankBalancesByDateRange(date, date)
	if err != nil {
		s.log.Error("get saved balances", sl.Err(err))
		return
	}
	have := make(map[string]bool, len(saved))
	for _, b := range saved {
		have[b.IBAN] = true
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	s.attempts++
	var failed []string
	for _, acc := range session.Accounts {
		if have[acc.IBAN] {
			continue
		}
		if err := s.fetch(ctx, acc, date, now); err != nil {
			s.log.Warn("fetch balances", sl.Err(err), slog.String("iban", acc.IBAN), slog.Int("attempt", s.attempts))
			failed = append(failed, acc.IBAN)
		}
	}

	switch {
	case len(failed) == 0:
		s.complete = true
		s.log.Info("bank balances saved", slog.String("date", date), slog.Bool("tg_skip", true))
	case s.attempts >= s.maxAttempts:
		s.log.With(slog.String("tg_topic", entity.TopicError)).Error("bank balances not fetched",
			slog.String("date", date),
			slog.String("ibans", strings.Join(failed, ",")),
			slog.Int("attempts", s.attempts))
	}
}

// fetch saves one account's balances for the day.
func (s *BalanceSnapshotter) fetch(ctx context.Context, acc entity.BankAccountRef, date string, now time.Time) error {
	balances, err := s.eb.Balances(ctx, acc.UID)
	if err != nil {
		return err
	}
	if len(balances) == 0 {
		return fmt.Errorf("bank returned no balances")
	}
	snap := &entity.BankBalanceSnapshot{Date: date, IBAN: acc.IBAN, Currency: acc.Currency, FetchedAt: now}
	for i := range balances {
		snap.Balances = append(snap.Balances, enablebanking.ToBankBalance(&balances[i]))
	}
	return s.db.SaveBankBalance(snap)
}

// BankBalanceReader serves stored snapshots to the API.
type BankBalanceReader interface {
	GetBankBalancesByDateRange(from, to string) ([]*entity.BankBalanceSnapshot, error)
	GetLatestBankBalances() ([]*entity.BankBalanceSnapshot, error)
}

// SetBankBalanceReader injects snapshot reads.
func (c *Core) SetBankBalanceReader(db BankBalanceReader) { c.bankBalanceDb = db }

// BankBalanceSnapshots returns stored snapshots for [from, to], or the latest snapshot
// of each account when both are empty.
func (c *Core) BankBalanceSnapshots(from, to string) ([]*entity.BankBalanceSnapshot, error) {
	if c.bankBalanceDb == nil {
		return nil, fmt.Errorf("bank balance storage is not available")
	}
	if from == "" && to == "" {
		return c.bankBalanceDb.GetLatestBankBalances()
	}
	return c.bankBalanceDb.GetBankBalancesByDateRange(from, to)
}
