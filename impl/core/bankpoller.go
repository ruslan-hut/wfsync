// Package core — bankpoller.go periodically fetches booked account movements from the
// bank and stores them, so that incoming payments can later be matched to the documents
// they settle.
//
// Three constraints shape it, all imposed from outside:
//
//   - Banks book entries with a back-date. Polling "since the last entry seen" would
//     silently miss those, so every run re-reads a sliding window and relies on the
//     idempotency key to collapse the overlap.
//   - PSD2 permits as few as four requests per account per day without the account
//     holder present. The configured interval is therefore an upper bound on ambition,
//     not a guarantee — a bank that refuses is reported, not retried into the ground.
//   - Consent expires within 180 days and cannot be refreshed. The poller cannot repair
//     that; all it can do is stop cleanly and make the need for a person visible early
//     enough to act on.
//
// Following the Start/Stop pattern of reconciler.go.
package core

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"wfsync/entity"
	"wfsync/internal/enablebanking"
	"wfsync/lib/sl"
)

// warnRepeatInterval throttles the re-authorization reminder. The consent lapses on a
// known date, so the reminder is a daily nudge, not a per-poll alarm.
const warnRepeatInterval = 24 * time.Hour

// BankStatementFetcher is the bank side of polling: the poller needs only these two
// operations, so it depends on them rather than on a concrete client.
type BankStatementFetcher interface {
	// Enabled reports whether the integration is configured.
	Enabled() bool
	// BankTransactions returns one account's booked movements within [from, to],
	// already normalized and keyed.
	BankTransactions(ctx context.Context, acc enablebanking.Account, from, to string) ([]*entity.BankTransaction, error)
}

// BankPollDatabase defines the persistence the poller needs.
type BankPollDatabase interface {
	GetActiveBankSession() (*entity.BankSession, error)
	SaveBankTransactions(txs []*entity.BankTransaction) (int, error)
	MarkBankSessionPolled(state string, at time.Time) error
	MarkBankSessionExpired(state, reason string) error
	MarkBankSessionWarned(state string, at time.Time) error
}

// BankPoller fetches account movements on a schedule.
type BankPoller struct {
	eb       BankStatementFetcher
	db       BankPollDatabase
	log      *slog.Logger
	interval time.Duration
	lookback int
	warnDays int
	// ibans limits polling to specific accounts; empty means every account the
	// consent covers.
	ibans   map[string]bool
	done    chan struct{}
	stopped chan struct{}
}

// NewBankPoller creates a poller. Call Start to begin background polling.
func NewBankPoller(eb BankStatementFetcher, log *slog.Logger, intervalMin, lookbackDays, warnDays int, ibans []string) *BankPoller {
	if intervalMin <= 0 {
		intervalMin = 30
	}
	if lookbackDays <= 0 {
		lookbackDays = 7
	}
	if warnDays <= 0 {
		warnDays = 30
	}
	filter := make(map[string]bool, len(ibans))
	for _, i := range ibans {
		if i != "" {
			filter[i] = true
		}
	}
	return &BankPoller{
		eb:       eb,
		log:      log.With(sl.Module("bankpoller")),
		interval: time.Duration(intervalMin) * time.Minute,
		lookback: lookbackDays,
		warnDays: warnDays,
		ibans:    filter,
	}
}

// SetDatabase injects persistence.
func (p *BankPoller) SetDatabase(db BankPollDatabase) { p.db = db }

// Start launches the background polling goroutine.
func (p *BankPoller) Start() {
	p.done = make(chan struct{})
	p.stopped = make(chan struct{})
	go func() {
		defer close(p.stopped)

		p.poll()

		ticker := time.NewTicker(p.interval)
		defer ticker.Stop()
		for {
			select {
			case <-p.done:
				p.log.Debug("bank poller stopped")
				return
			case <-ticker.C:
				p.poll()
			}
		}
	}()
}

// Stop signals the background goroutine to exit and waits for it to finish.
func (p *BankPoller) Stop() {
	if p.done != nil {
		p.log.Debug("stopping bank poller")
		close(p.done)
		<-p.stopped
	}
}

// poll runs one cycle: check the consent, fetch each account's window, store what came
// back.
func (p *BankPoller) poll() {
	if p.db == nil || p.eb == nil || !p.eb.Enabled() {
		return
	}

	session, err := p.db.GetActiveBankSession()
	if err != nil {
		p.log.Error("get active bank session", sl.Err(err))
		return
	}
	if session == nil {
		// Nothing is wrong — no account has been authorized yet. Silent, so an
		// unconfigured install does not fill the log.
		p.log.Debug("no authorized bank session", slog.Bool("tg_skip", true))
		return
	}

	now := time.Now()
	if !session.IsUsable(now) {
		p.expire(session, "consent is no longer valid")
		return
	}
	p.warnIfExpiring(session, now)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	from := now.AddDate(0, 0, -p.lookback).Format(time.DateOnly)
	to := now.Format(time.DateOnly)

	var fetched, stored int
	for _, acc := range session.Accounts {
		if len(p.ibans) > 0 && !p.ibans[acc.IBAN] {
			continue
		}
		n, s, err := p.pollAccount(ctx, acc, from, to)
		if err != nil {
			// A lapsed consent affects every account, so stop the cycle rather than
			// repeating the same failure per account.
			if errors.Is(err, enablebanking.ErrSessionExpired) {
				p.expire(session, err.Error())
				return
			}
			p.log.With(
				sl.Err(err),
				slog.String("iban", acc.IBAN),
				slog.String("tg_topic", entity.TopicError),
			).Error("poll bank account")
			continue
		}
		fetched += n
		stored += s
	}

	if err := p.db.MarkBankSessionPolled(session.State, now); err != nil {
		p.log.Error("mark session polled", sl.Err(err))
	}

	if stored == 0 {
		// Nothing new is the normal outcome of most polls; keep it out of Telegram.
		p.log.Debug("bank poll: nothing new",
			slog.Int("fetched", fetched),
			slog.Bool("tg_skip", true))
		return
	}

	p.log.With(slog.String("tg_topic", entity.TopicPayment)).Info("bank statement updated",
		slog.Int("new", stored),
		slog.Int("fetched", fetched),
		slog.String("from", from),
		slog.String("to", to))
}

// pollAccount fetches and stores one account's window, returning how many entries were
// fetched and how many of them were new.
func (p *BankPoller) pollAccount(ctx context.Context, acc entity.BankAccountRef, from, to string) (int, int, error) {
	txs, err := p.eb.BankTransactions(ctx, enablebanking.Account{
		UID:       acc.UID,
		Currency:  acc.Currency,
		AccountID: enablebanking.AccountID{IBAN: acc.IBAN},
	}, from, to)
	if err != nil {
		return 0, 0, err
	}

	stored, err := p.db.SaveBankTransactions(txs)
	if err != nil {
		return len(txs), 0, err
	}

	p.log.Debug("account polled",
		slog.String("iban", acc.IBAN),
		slog.Int("fetched", len(txs)),
		slog.Int("new", stored),
		slog.Bool("tg_skip", true))
	return len(txs), stored, nil
}

// warnIfExpiring nudges about an approaching re-authorization, at most once a day.
//
// The warning is the only thing standing between a lapsing consent and a silent halt of
// statement updates: renewal requires the account holder to sign in at the bank and
// approve access on their phone, which is not something that can be arranged in the
// hour after it stops working.
func (p *BankPoller) warnIfExpiring(session *entity.BankSession, now time.Time) {
	days := session.DaysLeft(now)
	if days > p.warnDays {
		return
	}
	if !session.LastWarnedAt.IsZero() && now.Sub(session.LastWarnedAt) < warnRepeatInterval {
		return
	}

	p.log.With(slog.String("tg_topic", entity.TopicSystem)).Warn("bank access expires soon",
		slog.Int("days_left", days),
		slog.String("valid_until", session.ValidUntil.Format(time.DateOnly)),
		slog.String("action", "re-authorize the account at the bank; access cannot be renewed automatically"))

	if err := p.db.MarkBankSessionWarned(session.State, now); err != nil {
		p.log.Error("mark session warned", sl.Err(err))
	}
}

// expire records a lapsed consent and stops further polling until someone re-authorizes.
func (p *BankPoller) expire(session *entity.BankSession, reason string) {
	if session.Status == entity.BankSessionExpired {
		// Already reported; stay quiet until a new consent replaces it.
		return
	}
	p.log.With(slog.String("tg_topic", entity.TopicError)).Error("bank access expired",
		slog.String("reason", reason),
		slog.String("action", "re-authorize the account: POST /v1/bank/auth and open the returned link"))

	if err := p.db.MarkBankSessionExpired(session.State, reason); err != nil {
		p.log.Error("mark session expired", sl.Err(err))
	}
}
