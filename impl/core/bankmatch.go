// Package core — bankmatch.go ties incoming bank transfers to the documents they settle.
//
// This is the part of the bank integration that carries the value. wFirma issues the
// proformas and invoices but exposes no account movements, so the only place the fact of
// payment exists is the statement — and the only thing linking a transfer to a document
// is what the customer typed in the payment reference.
//
// The cascade is shaped by what a month of real statements actually contained:
//
//   - A third of incoming payments were Stripe payouts. They aggregate many orders net of
//     fees and are already settled through webhooks, so they are classified out before
//     matching rather than left to clog the review queue.
//   - Where a document number was stated, it was correct every time. That makes the
//     number the match, and the amount merely a property of it.
//   - Customers paid documents issued months earlier, so the number lookup carries no
//     date bound.
//   - Most proformas are issued in EUR while payments arrive on a PLN account, converted
//     at the day's rate. Requiring the currencies to agree would reject correct matches,
//     so a currency difference is recorded rather than treated as a mismatch.
package core

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"

	"wfsync/entity"
	"wfsync/lib/sl"
)

// matchBatchLimit caps how many unresolved entries one pass considers, so a backlog
// cannot turn into an unbounded burst of database work.
const matchBatchLimit = 500

// docNumberPattern finds wFirma document numbers in a normalized payment reference.
// Matching runs on the whitespace-stripped form, so "PROF 74 5/2026" — a reference the
// bank broke mid-token when wrapping — still resolves. "PRO FORMA" spelled out counts as
// the PROF prefix; a number with no prefix at all is captured too, since customers often
// quote just "1137/2026", but it is only trusted once the amount confirms it.
var docNumberPattern = regexp.MustCompile(`(FV|PROF|PRO-?FORMA)?(\d+)/(\d{4})`)

// compactNumberPattern finds a document number written without the slash — "1100 2026"
// or "11452026" once whitespace is stripped. Digit runs on either side rule out IBANs and
// long references. Used only when no slashed number is present.
var compactNumberPattern = regexp.MustCompile(`(?:^|\D)(\d{1,5})(20[2-3]\d)(?:\D|$)`)

// orderNumberPattern finds an order number instead of a document number. Customers who
// do not quote the document quote the order, in Polish, German or English.
var orderNumberPattern = regexp.MustCompile(`(?:NUMERZAM[OÓ]WIENIA:?|ZAM[OÓ]WIENIE:?|BESTELLUNG:?|ORDER:?)(\d+)`)

// payoutParties are counterparties whose credits are settlements of an upstream payment
// processor, not customer payments. They aggregate many orders net of fees, so no single
// document corresponds to them, and the orders behind them are already closed by webhook.
var payoutParties = []string{"STRIPE"}

// BankMatchDatabase defines the persistence the matcher needs.
type BankMatchDatabase interface {
	GetUnmatchedBankTransactions(limit int) ([]*entity.BankTransaction, error)
	GetInvoicesByNumbers(numbers []string) ([]*entity.LocalInvoice, error)
	GetInvoicesByExternalRefs(refs []string) ([]*entity.LocalInvoice, error)
	GetBankTransactionByKey(key string) (*entity.BankTransaction, error)
	SetBankTransactionMatch(key, status, ref string) error
	SavePaymentFact(fact *entity.PaymentFact) error
	GetPaymentFactsByRefs(refs []string) ([]*entity.PaymentFact, error)
}

// BankFactDatabase provides read access to settlement records for the ERP feed.
type BankFactDatabase interface {
	GetPaymentFactsByDateRange(from, to time.Time) ([]*entity.PaymentFact, error)
}

// SetBankMatchDatabase injects the matcher's persistence.
func (c *Core) SetBankMatchDatabase(db BankMatchDatabase) { c.bankMatchDb = db }

// SetBankFactDatabase injects settlement-record reads.
func (c *Core) SetBankFactDatabase(db BankFactDatabase) { c.bankFactDb = db }

// BankMatchResult summarises one matching pass.
type BankMatchResult struct {
	Scanned   int `json:"scanned"`
	Matched   int `json:"matched"`
	Ignored   int `json:"ignored"`
	Unmatched int `json:"unmatched"`
}

// MatchBankPayments resolves unmatched incoming transfers against issued documents and
// records the resulting payment facts. Safe to run repeatedly: a resolved entry leaves
// the queue, so a pass only ever sees what is still open.
func (c *Core) MatchBankPayments(ctx context.Context) (*BankMatchResult, error) {
	if c.bankMatchDb == nil {
		return nil, fmt.Errorf("bank match storage is not available")
	}

	txs, err := c.bankMatchDb.GetUnmatchedBankTransactions(matchBatchLimit)
	if err != nil {
		return nil, fmt.Errorf("get unmatched transactions: %w", err)
	}
	result := &BankMatchResult{Scanned: len(txs)}
	if len(txs) == 0 {
		return result, nil
	}

	// Entries that are not customer payments never reach the document lookup.
	payments := make([]*entity.BankTransaction, 0, len(txs))
	for _, tx := range txs {
		if reason, ignore := classifyIgnored(tx); ignore {
			if err := c.bankMatchDb.SetBankTransactionMatch(tx.Key, entity.BankMatchIgnored, ""); err != nil {
				c.log.Error("mark transaction ignored", sl.Err(err), slog.String("key", tx.Key))
				continue
			}
			c.log.Debug("transaction ignored",
				slog.String("reason", reason),
				slog.String("party", tx.PartyName),
				slog.Bool("tg_skip", true))
			result.Ignored++
			continue
		}
		payments = append(payments, tx)
	}
	if len(payments) == 0 {
		return result, nil
	}

	docs, err := c.loadCandidateDocuments(payments)
	if err != nil {
		return nil, err
	}

	for _, tx := range payments {
		allocs := matchDocuments(tx, docs)
		if len(allocs) == 0 {
			result.Unmatched++
			continue
		}
		if err := c.recordAllocations(tx, allocs, entity.MatchExact); err != nil {
			c.log.Error("record payment fact", sl.Err(err), slog.String("key", tx.Key))
			result.Unmatched++
			continue
		}
		result.Matched++
	}

	if result.Matched > 0 || result.Unmatched > 0 {
		c.log.With(slog.String("tg_topic", entity.TopicPayment)).Info("bank payments matched",
			slog.Int("matched", result.Matched),
			slog.Int("unmatched", result.Unmatched),
			slog.Int("ignored", result.Ignored))
	}
	return result, nil
}

// classifyIgnored reports whether an entry is something other than a customer payment,
// and why.
func classifyIgnored(tx *entity.BankTransaction) (string, bool) {
	if !tx.IsCredit() {
		return "not an incoming payment", true
	}
	party := strings.ToUpper(tx.PartyName)
	for _, p := range payoutParties {
		if strings.Contains(party, p) {
			return "payment processor payout", true
		}
	}
	return "", false
}

// docRef is one document number found in a payment reference, with the stored forms it
// may correspond to. A bare number names no document type, so it yields both a proforma
// and an invoice candidate; the amount decides between them.
type docRef struct {
	candidates []string // normalized document numbers, e.g. "PROF1103/2026"
	bare       bool
}

// documentRefs extracts the document numbers a reference quotes, in order, without
// duplicates.
func documentRefs(key string) []docRef {
	var refs []docRef
	seen := map[string]bool{}
	add := func(prefix, num, year string) {
		id := num + "/" + year
		if seen[id] {
			return
		}
		seen[id] = true
		switch {
		case prefix == "FV":
			refs = append(refs, docRef{candidates: []string{"FV" + id}})
		case prefix != "":
			refs = append(refs, docRef{candidates: []string{"PROF" + id}})
		default:
			refs = append(refs, docRef{candidates: []string{"PROF" + id, "FV" + id}, bare: true})
		}
	}

	for _, m := range docNumberPattern.FindAllStringSubmatch(key, -1) {
		add(m[1], m[2], m[3])
	}
	if len(refs) == 0 {
		if m := compactNumberPattern.FindStringSubmatch(key); m != nil {
			add("", m[1], m[2])
		}
	}
	return refs
}

// spacedNumber turns a normalized number into the stored form, "PROF1103/2026" into
// "PROF 1103/2026".
func spacedNumber(n string) string {
	for _, p := range []string{"PROF", "FV"} {
		if rest, ok := strings.CutPrefix(n, p); ok {
			return p + " " + rest
		}
	}
	return n
}

// loadCandidateDocuments fetches every document referenced by the batch in two queries,
// keyed for lookup. Numbers are read in their normalized form, since that is what the
// payment reference yields.
func (c *Core) loadCandidateDocuments(txs []*entity.BankTransaction) (map[string]*entity.LocalInvoice, error) {
	numberSet := map[string]bool{}
	refSet := map[string]bool{}

	for _, tx := range txs {
		for _, r := range documentRefs(tx.RemittanceKey) {
			for _, n := range r.candidates {
				// The stored number carries a space the reference may not ("FV 768/2026"
				// vs "FV768/2026"); query for both forms rather than scanning with a regex.
				numberSet[n] = true
				numberSet[spacedNumber(n)] = true
			}
		}
		if m := orderNumberPattern.FindStringSubmatch(tx.RemittanceKey); m != nil {
			refSet[m[1]] = true
		}
	}

	docs := map[string]*entity.LocalInvoice{}

	if len(numberSet) > 0 {
		byNumber, err := c.bankMatchDb.GetInvoicesByNumbers(keysOf(numberSet))
		if err != nil {
			return nil, fmt.Errorf("get invoices by number: %w", err)
		}
		for _, d := range byNumber {
			docs["N:"+entity.NormalizeRemittance(d.Number)] = d
		}
	}

	if len(refSet) > 0 {
		byRef, err := c.bankMatchDb.GetInvoicesByExternalRefs(keysOf(refSet))
		if err != nil {
			return nil, fmt.Errorf("get invoices by external ref: %w", err)
		}
		for _, d := range byRef {
			// An order split across several documents yields more than one row; the
			// first is enough to name the order, which is what the reference asked for.
			key := "R:" + entity.NormalizeRemittance(d.IdExternal)
			if _, seen := docs[key]; !seen {
				docs[key] = d
			}
		}
	}

	return docs, nil
}

// allocation is the part of a transfer credited to one document.
type allocation struct {
	doc    *entity.LocalInvoice
	amount int64
}

// matchDocuments resolves one payment to the documents it settles, by the numbers stated
// in its reference.
//
// Only level 1 of the cascade is automatic. Matching by amount and counterparty alone is
// a guess, and a wrong guess here marks somebody else's order paid — so those land in the
// review queue instead, where a person decides. The amount is consulted only to confirm
// what the reference already says:
//
//   - One prefixed number is trusted as is, whatever the amount or currency.
//   - One bare number is accepted only when exactly one candidate has the payment's
//     currency and total.
//   - Several numbers are each resolved separately, and the transfer is split by
//     document total — only when every document is found, each unambiguously, in the
//     payment's currency, and the totals add up to the payment exactly.
func matchDocuments(tx *entity.BankTransaction, docs map[string]*entity.LocalInvoice) []allocation {
	refs := documentRefs(tx.RemittanceKey)

	if len(refs) == 1 && !refs[0].bare {
		if d, ok := docs["N:"+refs[0].candidates[0]]; ok {
			return []allocation{{doc: d, amount: tx.Amount}}
		}
	}

	// An order number is stated explicitly, so it outranks a bare number, which may be
	// nothing more than the same digits read as "number + year".
	if m := orderNumberPattern.FindStringSubmatch(tx.RemittanceKey); m != nil {
		if d, ok := docs["R:"+m[1]]; ok {
			return []allocation{{doc: d, amount: tx.Amount}}
		}
	}

	switch {
	case len(refs) == 1 && refs[0].bare:
		if d := pickCandidate(refs[0], docs, tx.Currency, tx.Amount); d != nil {
			return []allocation{{doc: d, amount: tx.Amount}}
		}
	case len(refs) > 1:
		return splitByDocuments(tx, refs, docs)
	}
	return nil
}

// pickCandidate returns the only document among a reference's candidates in the given
// currency — and, when amount is positive, with exactly that total. Ambiguity yields nil.
func pickCandidate(r docRef, docs map[string]*entity.LocalInvoice, currency string, amount int64) *entity.LocalInvoice {
	var found *entity.LocalInvoice
	for _, n := range r.candidates {
		d, ok := docs["N:"+n]
		if !ok || !strings.EqualFold(d.Currency, currency) {
			continue
		}
		if amount > 0 && toMinorUnits(d.Total) != amount {
			continue
		}
		if found != nil {
			return nil
		}
		found = d
	}
	return found
}

// splitByDocuments credits each quoted document with its own total. Documents of the
// same order are merged, since the settlement record is kept per order.
func splitByDocuments(tx *entity.BankTransaction, refs []docRef, docs map[string]*entity.LocalInvoice) []allocation {
	var allocs []allocation
	byOrder := map[string]int{}
	var sum int64
	for _, r := range refs {
		d := pickCandidate(r, docs, tx.Currency, 0)
		if d == nil {
			return nil
		}
		total := toMinorUnits(d.Total)
		sum += total
		if i, ok := byOrder[d.IdExternal]; ok {
			allocs[i].amount += total
			continue
		}
		byOrder[d.IdExternal] = len(allocs)
		allocs = append(allocs, allocation{doc: d, amount: total})
	}
	if sum != tx.Amount {
		return nil
	}
	return allocs
}

// recordAllocations writes a settlement record per allocation, then marks the statement
// entry resolved — last, so a failure part-way leaves the entry in the queue for the next
// pass, which the per-record idempotency makes safe to repeat.
func (c *Core) recordAllocations(tx *entity.BankTransaction, allocs []allocation, level string) error {
	refs := make([]string, 0, len(allocs))
	for _, a := range allocs {
		ref, err := c.recordPayment(tx, a.doc, a.amount, level)
		if err != nil {
			return err
		}
		refs = append(refs, ref)
	}
	if err := c.bankMatchDb.SetBankTransactionMatch(tx.Key, entity.BankMatchMatched, strings.Join(refs, ",")); err != nil {
		return fmt.Errorf("mark transaction matched: %w", err)
	}
	return nil
}

// recordPayment adds amount of the transfer to the settlement record of the order the
// document belongs to, and returns that order's reference.
//
// Payments accumulate: an order can be settled by several transfers, and each pass adds
// to what previous ones recorded rather than replacing it.
func (c *Core) recordPayment(tx *entity.BankTransaction, doc *entity.LocalInvoice, amount int64, level string) (string, error) {
	ref := doc.IdExternal
	if ref == "" {
		return "", fmt.Errorf("document %s has no external reference", doc.Number)
	}

	fact := &entity.PaymentFact{ExternalRef: ref}
	if existing, err := c.bankMatchDb.GetPaymentFactsByRefs([]string{ref}); err != nil {
		return "", fmt.Errorf("load payment fact: %w", err)
	} else if len(existing) > 0 {
		fact = existing[0]
	}

	// Re-running a pass must not double-count a transfer already recorded.
	for _, k := range fact.TransactionKeys {
		if k == tx.Key {
			return ref, nil
		}
	}

	fact.DocumentNumber = doc.Number
	fact.DocumentID = doc.Id
	fact.DocumentType = doc.Type
	fact.CurrencyDoc = strings.ToUpper(doc.Currency)
	fact.AmountDue, fact.DocumentNumbers = c.orderDue(doc)
	fact.CurrencyPaid = tx.Currency
	fact.AmountPaid += amount
	fact.TransactionKeys = append(fact.TransactionKeys, tx.Key)
	fact.PartyName = tx.PartyName
	fact.MatchLevel = level
	fact.UpdatedAt = time.Now()
	if paidAt, err := time.Parse(time.DateOnly, tx.BookingDate); err == nil && paidAt.After(fact.PaidAt) {
		fact.PaidAt = paidAt
	}
	fact.OrderNumber = ref

	fact.AmountStatus, fact.Status = settlement(fact)

	if err := c.bankMatchDb.SavePaymentFact(fact); err != nil {
		return "", fmt.Errorf("save payment fact: %w", err)
	}

	c.log.Debug("payment matched",
		slog.String("document", doc.Number),
		slog.String("external_ref", ref),
		slog.Int64("paid", fact.AmountPaid),
		slog.Int64("due", fact.AmountDue),
		slog.String("amount_status", fact.AmountStatus),
		slog.Bool("tg_skip", true))
	return ref, nil
}

// orderDue returns what the order behind doc asks for, and the documents that make it up.
//
// The settlement record is kept per order, but a large B2B order is issued as a set of
// proformas sharing one id_external, and customers pay the set. Comparing the payment
// with one document of it would report a fully paid order as overpaid. The set is taken
// from the B2B proforma record rather than from every local document of the order: a
// reissued set leaves its deleted predecessors behind in the local copy. Anything that
// cannot be resolved completely falls back to doc alone.
func (c *Core) orderDue(doc *entity.LocalInvoice) (int64, []string) {
	single, singleNumbers := toMinorUnits(doc.Total), []string{doc.Number}
	if c.proformaDb == nil || doc.IdExternal == "" {
		return single, singleNumbers
	}

	rec, err := c.proformaDb.GetProformaRecord(doc.IdExternal)
	if err != nil {
		c.log.Warn("load proforma record", sl.Err(err), slog.String("external_ref", doc.IdExternal))
		return single, singleNumbers
	}
	ids := rec.DocumentIds()
	if len(ids) < 2 || !slices.Contains(ids, doc.Id) {
		return single, singleNumbers
	}

	local, err := c.bankMatchDb.GetInvoicesByExternalRefs([]string{doc.IdExternal})
	if err != nil {
		c.log.Warn("load order documents", sl.Err(err), slog.String("external_ref", doc.IdExternal))
		return single, singleNumbers
	}
	byID := make(map[string]*entity.LocalInvoice, len(local))
	for _, d := range local {
		byID[d.Id] = d
	}
	parts := map[string]*entity.Payment{}
	for _, p := range rec.Payment.Parts {
		if p != nil {
			parts[p.Id] = p
		}
	}

	var due int64
	numbers := make([]string, 0, len(ids))
	for _, id := range ids {
		if d, ok := byID[id]; ok && strings.EqualFold(d.Currency, doc.Currency) {
			due += toMinorUnits(d.Total)
			numbers = append(numbers, d.Number)
			continue
		}
		// Records issued since document amounts were kept carry them; older ones do not.
		if p, ok := parts[id]; ok && p.Amount > 0 && strings.EqualFold(p.Currency, doc.Currency) {
			due += p.Amount
			numbers = append(numbers, p.Number)
			continue
		}
		return single, singleNumbers
	}
	return due, numbers
}

// RecalculatePaymentDue re-derives the amount due of every settlement record from its
// order's current document set, and returns how many records changed.
//
// Records written before the whole set was counted show a split B2B order as overpaid.
// Their transfers are already resolved, so no matching pass revisits them. Idempotent,
// so it is safe to run on every start.
func (c *Core) RecalculatePaymentDue() (int, error) {
	if c.bankMatchDb == nil || c.bankFactDb == nil {
		return 0, fmt.Errorf("bank match storage is not available")
	}
	facts, err := c.bankFactDb.GetPaymentFactsByDateRange(time.Time{}, time.Now().AddDate(1, 0, 0))
	if err != nil {
		return 0, fmt.Errorf("get payment facts: %w", err)
	}

	changed := 0
	for _, fact := range facts {
		if fact.DocumentID == "" || fact.ExternalRef == "" {
			continue
		}
		docs, err := c.bankMatchDb.GetInvoicesByExternalRefs([]string{fact.ExternalRef})
		if err != nil {
			return changed, fmt.Errorf("get order documents: %w", err)
		}
		i := slices.IndexFunc(docs, func(d *entity.LocalInvoice) bool { return d.Id == fact.DocumentID })
		if i < 0 {
			continue
		}

		due, numbers := c.orderDue(docs[i])
		if due == fact.AmountDue && slices.Equal(numbers, fact.DocumentNumbers) {
			continue
		}
		fact.AmountDue, fact.DocumentNumbers = due, numbers
		fact.AmountStatus, fact.Status = settlement(fact)
		fact.UpdatedAt = time.Now()
		if err := c.bankMatchDb.SavePaymentFact(fact); err != nil {
			return changed, fmt.Errorf("save payment fact: %w", err)
		}
		changed++
	}
	return changed, nil
}

// settlement derives how the paid sum compares with the document, and the resulting
// order state.
//
// A currency difference short-circuits the comparison: the customer paid a EUR document
// in PLN at the day's rate, so the two sums are not comparable without that rate. The
// document is still identified correctly, which is why this is reported as a property of
// the amount rather than as a failed match. Closing such an order automatically would
// need a rate source; until then it is left for a person.
func settlement(f *entity.PaymentFact) (amountStatus, status string) {
	if f.CurrencyDoc != "" && f.CurrencyPaid != "" && f.CurrencyDoc != f.CurrencyPaid {
		return entity.AmountFX, entity.PaymentFactPartial
	}
	switch {
	case f.AmountDue <= 0:
		return entity.AmountExact, entity.PaymentFactPaid
	case f.AmountPaid < f.AmountDue:
		return entity.AmountPartial, entity.PaymentFactPartial
	case f.AmountPaid > f.AmountDue:
		return entity.AmountOver, entity.PaymentFactOverpaid
	default:
		return entity.AmountExact, entity.PaymentFactPaid
	}
}

// UnmatchedBankPayments returns incoming payments no document could be found for — the
// manual review queue.
func (c *Core) UnmatchedBankPayments(limit int) ([]*entity.BankTransaction, error) {
	if c.bankMatchDb == nil {
		return nil, fmt.Errorf("bank match storage is not available")
	}
	return c.bankMatchDb.GetUnmatchedBankTransactions(limit)
}

// MatchBankPaymentManually links one statement entry to a document by hand, for the
// payments the cascade could not resolve. Without it the review queue only accumulates.
func (c *Core) MatchBankPaymentManually(ctx context.Context, txKey, documentNumber string) error {
	if c.bankMatchDb == nil {
		return fmt.Errorf("bank match storage is not available")
	}

	tx, err := c.bankMatchDb.GetBankTransactionByKey(txKey)
	if err != nil {
		return fmt.Errorf("get transaction: %w", err)
	}
	if tx == nil {
		return fmt.Errorf("transaction %s not found", txKey)
	}

	docs, err := c.bankMatchDb.GetInvoicesByNumbers([]string{documentNumber})
	if err != nil {
		return fmt.Errorf("get document: %w", err)
	}
	if len(docs) == 0 {
		return fmt.Errorf("document %s not found", documentNumber)
	}

	return c.recordAllocations(tx, []allocation{{doc: docs[0], amount: tx.Amount}}, entity.MatchManual)
}

// PaymentFacts returns settlement records for the given orders, which is how the B2B
// portal and the shop ask whether an order was paid.
func (c *Core) PaymentFacts(refs []string) ([]*entity.PaymentFact, error) {
	if c.bankMatchDb == nil {
		return nil, fmt.Errorf("bank match storage is not available")
	}
	return c.bankMatchDb.GetPaymentFactsByRefs(refs)
}

// toMinorUnits converts a document total to minor units.
func toMinorUnits(v float64) int64 {
	return int64(math.Round(v * 100))
}

// keysOf returns the keys of a set, order unspecified.
func keysOf(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	return out
}

// PaymentFactsByDateRange returns settlement records whose payment landed within the
// range. This is the ERP feed: the fact that an order was paid, as opposed to the raw
// statement served by BankTransactions.
func (c *Core) PaymentFactsByDateRange(from, to time.Time) ([]*entity.PaymentFact, error) {
	if c.bankFactDb == nil {
		return nil, fmt.Errorf("bank match storage is not available")
	}
	return c.bankFactDb.GetPaymentFactsByDateRange(from, to)
}
