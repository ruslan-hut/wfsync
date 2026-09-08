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
	"strings"
	"time"

	"wfsync/entity"
	"wfsync/lib/sl"
)

// matchBatchLimit caps how many unresolved entries one pass considers, so a backlog
// cannot turn into an unbounded burst of database work.
const matchBatchLimit = 500

// docNumberPattern finds a wFirma document number in a normalized payment reference.
// Matching runs on the whitespace-stripped form, so "PROF 74 5/2026" — a reference the
// bank broke mid-token when wrapping — still resolves.
var docNumberPattern = regexp.MustCompile(`(FV|PROF)(\d+)/(\d{4})`)

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

// SetOpenCartPaidStatus sets the order status applied when a bank transfer settles a
// shop order. Zero disables the update.
func (c *Core) SetOpenCartPaidStatus(status int) { c.ocPaidStatus = status }

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
		doc := matchDocument(tx, docs)
		if doc == nil {
			result.Unmatched++
			continue
		}
		if err := c.recordPayment(ctx, tx, doc, entity.MatchExact); err != nil {
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

// loadCandidateDocuments fetches every document referenced by the batch in two queries,
// keyed for lookup. Numbers are read in their normalized form, since that is what the
// payment reference yields.
func (c *Core) loadCandidateDocuments(txs []*entity.BankTransaction) (map[string]*entity.LocalInvoice, error) {
	numberSet := map[string]bool{}
	refSet := map[string]bool{}

	for _, tx := range txs {
		if m := docNumberPattern.FindStringSubmatch(tx.RemittanceKey); m != nil {
			// The stored number carries a space the reference may not ("FV 768/2026"
			// vs "FV768/2026"); query for both forms rather than scanning with a regex.
			numberSet[fmt.Sprintf("%s %s/%s", m[1], m[2], m[3])] = true
			numberSet[fmt.Sprintf("%s%s/%s", m[1], m[2], m[3])] = true
			continue
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

// matchDocument resolves one payment to a document by the number stated in its reference.
//
// Only level 1 of the cascade is automatic. Matching by amount and counterparty alone is
// a guess, and a wrong guess here marks somebody else's order paid — so those land in the
// review queue instead, where a person decides.
func matchDocument(tx *entity.BankTransaction, docs map[string]*entity.LocalInvoice) *entity.LocalInvoice {
	if m := docNumberPattern.FindStringSubmatch(tx.RemittanceKey); m != nil {
		if d, ok := docs["N:"+m[0]]; ok {
			return d
		}
	}
	if m := orderNumberPattern.FindStringSubmatch(tx.RemittanceKey); m != nil {
		if d, ok := docs["R:"+m[1]]; ok {
			return d
		}
	}
	return nil
}

// recordPayment writes the settlement record for the order the document belongs to, then
// marks the statement entry resolved.
//
// Payments accumulate: an order can be settled by several transfers, and each pass adds
// to what previous ones recorded rather than replacing it.
func (c *Core) recordPayment(_ context.Context, tx *entity.BankTransaction, doc *entity.LocalInvoice, level string) error {
	ref := doc.IdExternal
	if ref == "" {
		return fmt.Errorf("document %s has no external reference", doc.Number)
	}

	fact := &entity.PaymentFact{ExternalRef: ref}
	if existing, err := c.bankMatchDb.GetPaymentFactsByRefs([]string{ref}); err != nil {
		return fmt.Errorf("load payment fact: %w", err)
	} else if len(existing) > 0 {
		fact = existing[0]
	}

	// Re-running a pass must not double-count a transfer already recorded.
	for _, k := range fact.TransactionKeys {
		if k == tx.Key {
			return c.bankMatchDb.SetBankTransactionMatch(tx.Key, entity.BankMatchMatched, ref)
		}
	}

	fact.DocumentNumber = doc.Number
	fact.DocumentID = doc.Id
	fact.DocumentType = doc.Type
	fact.CurrencyDoc = strings.ToUpper(doc.Currency)
	fact.AmountDue = toMinorUnits(doc.Total)
	fact.CurrencyPaid = tx.Currency
	fact.AmountPaid += tx.Amount
	fact.TransactionKeys = append(fact.TransactionKeys, tx.Key)
	fact.PartyName = tx.PartyName
	fact.MatchLevel = level
	fact.UpdatedAt = time.Now()
	if paidAt, err := time.Parse(time.DateOnly, tx.BookingDate); err == nil && paidAt.After(fact.PaidAt) {
		fact.PaidAt = paidAt
	}
	if doc.IdExternal != "" {
		fact.OrderNumber = doc.IdExternal
	}

	fact.AmountStatus, fact.Status = settlement(fact)

	if err := c.bankMatchDb.SavePaymentFact(fact); err != nil {
		return fmt.Errorf("save payment fact: %w", err)
	}
	if err := c.bankMatchDb.SetBankTransactionMatch(tx.Key, entity.BankMatchMatched, ref); err != nil {
		return fmt.Errorf("mark transaction matched: %w", err)
	}

	c.reflectInOpenCart(fact)

	c.log.Debug("payment matched",
		slog.String("document", doc.Number),
		slog.String("external_ref", ref),
		slog.Int64("paid", fact.AmountPaid),
		slog.Int64("due", fact.AmountDue),
		slog.String("amount_status", fact.AmountStatus),
		slog.Bool("tg_skip", true))
	return nil
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

	return c.recordPayment(ctx, tx, docs[0], entity.MatchManual)
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

// reflectInOpenCart moves a shop order to the paid status once a transfer settles it.
//
// Guarded three ways, because moving the wrong order is worse than moving none. Only a
// fully settled order qualifies, only an exact or manual match (a probable one is a
// guess), and only a numeric reference — the shop's order ids are numeric while the B2B
// portal's are opaque UIDs, so anything else belongs to a system OpenCart knows nothing
// about.
func (c *Core) reflectInOpenCart(fact *entity.PaymentFact) {
	if c.oc == nil || c.ocPaidStatus <= 0 {
		return
	}
	if !fact.Settled() || !isNumericRef(fact.ExternalRef) {
		return
	}

	comment := fmt.Sprintf("Bank transfer received: %d %s (%s)",
		fact.AmountPaid, fact.CurrencyPaid, fact.DocumentNumber)
	if err := c.oc.ChangeOrderStatus(fact.ExternalRef, c.ocPaidStatus, comment); err != nil {
		c.log.With(
			sl.Err(err),
			slog.String("order_id", fact.ExternalRef),
		).Error("change order status after bank payment")
		return
	}
	c.log.Info("order marked paid by bank transfer",
		slog.String("order_id", fact.ExternalRef),
		slog.String("document", fact.DocumentNumber))
}

// isNumericRef reports whether the external reference is a shop order id rather than a
// B2B order UID.
func isNumericRef(ref string) bool {
	if ref == "" {
		return false
	}
	for _, r := range ref {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
