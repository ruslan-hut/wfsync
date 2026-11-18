package core

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"wfsync/entity"
)

// fakeMatchDB stands in for storage, keyed the way the real queries are.
type fakeMatchDB struct {
	unmatched []*entity.BankTransaction
	byNumber  map[string]*entity.LocalInvoice
	byRef     map[string]*entity.LocalInvoice
	facts     map[string]*entity.PaymentFact
	marks     map[string]string // tx key → match status
}

func newFakeMatchDB() *fakeMatchDB {
	return &fakeMatchDB{
		byNumber: map[string]*entity.LocalInvoice{},
		byRef:    map[string]*entity.LocalInvoice{},
		facts:    map[string]*entity.PaymentFact{},
		marks:    map[string]string{},
	}
}

func (f *fakeMatchDB) GetUnmatchedBankTransactions(int) ([]*entity.BankTransaction, error) {
	return f.unmatched, nil
}
func (f *fakeMatchDB) GetInvoicesByNumbers(numbers []string) ([]*entity.LocalInvoice, error) {
	var out []*entity.LocalInvoice
	seen := map[string]bool{}
	for _, n := range numbers {
		if d, ok := f.byNumber[n]; ok && !seen[d.Number] {
			seen[d.Number] = true
			out = append(out, d)
		}
	}
	return out, nil
}
func (f *fakeMatchDB) GetInvoicesByExternalRefs(refs []string) ([]*entity.LocalInvoice, error) {
	var out []*entity.LocalInvoice
	for _, r := range refs {
		if d, ok := f.byRef[r]; ok {
			out = append(out, d)
		}
	}
	return out, nil
}
func (f *fakeMatchDB) GetBankTransactionByKey(key string) (*entity.BankTransaction, error) {
	for _, t := range f.unmatched {
		if t.Key == key {
			return t, nil
		}
	}
	return nil, nil
}
func (f *fakeMatchDB) SetBankTransactionMatch(key, status, _ string) error {
	f.marks[key] = status
	return nil
}
func (f *fakeMatchDB) SavePaymentFact(fact *entity.PaymentFact) error {
	f.facts[fact.ExternalRef] = fact
	return nil
}
func (f *fakeMatchDB) GetPaymentFactsByRefs(refs []string) ([]*entity.PaymentFact, error) {
	var out []*entity.PaymentFact
	for _, r := range refs {
		if fact, ok := f.facts[r]; ok {
			out = append(out, fact)
		}
	}
	return out, nil
}

func testMatcher(db BankMatchDatabase) *Core {
	c := &Core{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	c.SetBankMatchDatabase(db)
	return c
}

// credit builds an incoming payment with the reference already normalized the way the
// mappers do.
func credit(key, remittance, party string, amount int64, currency string) *entity.BankTransaction {
	tx := &entity.BankTransaction{
		Key:         key,
		Direction:   entity.DirectionCredit,
		Amount:      amount,
		Currency:    currency,
		BookingDate: "2026-08-31",
		PartyName:   party,
	}
	tx.SetRemittance(remittance)
	return tx
}

func doc(number, ref, currency string, total float64) *entity.LocalInvoice {
	return &entity.LocalInvoice{
		Id: "id-" + number, Number: number, IdExternal: ref,
		Currency: currency, Total: total, Type: "proforma",
	}
}

// TestMatchReferenceForms covers the reference shapes a real month of statements
// contained — the plain form, the wrapped one PKO produces, a SWIFT-prefixed foreign
// transfer, and an order number instead of a document number.
func TestMatchReferenceForms(t *testing.T) {
	tests := []struct {
		name       string
		remittance string
		wantDoc    string
	}{
		{"plain proforma", "Pro forma nr PROF 765/2026", "PROF 765/2026"},
		{"uppercase variant", "PRO FORMA NR PROF 765/2026", "PROF 765/2026"},
		{"bare number", "Prof 765/2026", "PROF 765/2026"},
		{"invoice", "Faktura nr FV 768/2026", "FV 768/2026"},
		{"invoice, other wording", "Faktura numer FV 768/2026", "FV 768/2026"},
		// The bank wraps at a fixed width and joins with a space, breaking the number.
		{"number split by wrap", "Pro forma nr PROF 76 5/2026", "PROF 765/2026"},
		// Foreign transfers arrive with a SWIFT reference glued in front.
		{"swift reference", "/REF/281475550255106/PROF 765/2026 . DARK NAIL GELS", "PROF 765/2026"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newFakeMatchDB()
			d := doc(tt.wantDoc, "order-1", "PLN", 100.00)
			db.byNumber[tt.wantDoc] = d
			db.byNumber[entity.NormalizeRemittance(tt.wantDoc)] = d
			db.unmatched = []*entity.BankTransaction{credit("k1", tt.remittance, "CLIENT", 10000, "PLN")}

			res, err := testMatcher(db).MatchBankPayments(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if res.Matched != 1 {
				t.Fatalf("matched %d, want 1 (unmatched %d)", res.Matched, res.Unmatched)
			}
			if got := db.facts["order-1"].DocumentNumber; got != tt.wantDoc {
				t.Errorf("document = %q, want %q", got, tt.wantDoc)
			}
		})
	}
}

// TestMatchOrderNumber covers customers who quote the order instead of the document.
func TestMatchOrderNumber(t *testing.T) {
	for _, remit := range []string{"Numer zamówienia: 17216, Edyta Malesa", "BESTELLUNG 17102"} {
		t.Run(remit, func(t *testing.T) {
			db := newFakeMatchDB()
			db.byRef["17216"] = doc("FV 900/2026", "17216", "PLN", 303.99)
			db.byRef["17102"] = doc("FV 901/2026", "17102", "PLN", 118.577)
			db.unmatched = []*entity.BankTransaction{credit("k1", remit, "CLIENT", 30399, "PLN")}

			res, err := testMatcher(db).MatchBankPayments(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if res.Matched != 1 {
				t.Errorf("matched %d, want 1", res.Matched)
			}
		})
	}
}

// TestPayoutsAreIgnored guards the biggest source of noise: a third of incoming payments
// are processor payouts that aggregate many orders net of fees. They must never reach the
// review queue, or it fills with entries nobody can act on.
func TestPayoutsAreIgnored(t *testing.T) {
	db := newFakeMatchDB()
	db.unmatched = []*entity.BankTransaction{
		credit("k1", "STRIPE", "STRIPE TECHNOLOGY EUROPE LTD 3 DUBLIN LA", 349140, "PLN"),
	}

	res, err := testMatcher(db).MatchBankPayments(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Ignored != 1 || res.Unmatched != 0 {
		t.Errorf("ignored=%d unmatched=%d, want 1 and 0", res.Ignored, res.Unmatched)
	}
	if db.marks["k1"] != entity.BankMatchIgnored {
		t.Errorf("mark = %q, want %q", db.marks["k1"], entity.BankMatchIgnored)
	}
}

// TestUnknownReferenceGoesToQueue covers a payment that is not for an order at all — a
// refund from a supplier, say. It must land in the queue rather than be forced onto some
// document by amount.
func TestUnknownReferenceGoesToQueue(t *testing.T) {
	db := newFakeMatchDB()
	db.byNumber["PROF 765/2026"] = doc("PROF 765/2026", "order-1", "PLN", 150.00)
	db.unmatched = []*entity.BankTransaction{
		credit("k1", "zwrot badania medycyny pracy", "MSG SIEDLECKI GROUP", 15000, "PLN"),
	}

	res, err := testMatcher(db).MatchBankPayments(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Unmatched != 1 || res.Matched != 0 {
		t.Errorf("unmatched=%d matched=%d, want 1 and 0", res.Unmatched, res.Matched)
	}
	if _, ok := db.facts["order-1"]; ok {
		t.Error("an equal amount was enough to settle an unrelated document")
	}
}

func TestSettlementStatus(t *testing.T) {
	tests := []struct {
		name           string
		docCurrency    string
		docTotal       float64
		paidCurrency   string
		paid           int64
		wantAmount     string
		wantStatus     string
		wantSettleable bool
	}{
		{"exact", "PLN", 362.78, "PLN", 36278, entity.AmountExact, entity.PaymentFactPaid, true},
		{"partial", "PLN", 1000.00, "PLN", 10000, entity.AmountPartial, entity.PaymentFactPartial, false},
		{"overpaid", "PLN", 100.00, "PLN", 15000, entity.AmountOver, entity.PaymentFactOverpaid, true},
		{
			// A EUR proforma settled in PLN at the day's rate. The document is identified
			// beyond doubt, but the sums are not comparable without a rate, so this is
			// left for a person rather than closed automatically.
			name: "cross-currency", docCurrency: "EUR", docTotal: 3212.56,
			paidCurrency: "PLN", paid: 1329711,
			wantAmount: entity.AmountFX, wantStatus: entity.PaymentFactPartial, wantSettleable: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newFakeMatchDB()
			db.byNumber["PROF 700/2026"] = doc("PROF 700/2026", "order-1", tt.docCurrency, tt.docTotal)
			db.unmatched = []*entity.BankTransaction{
				credit("k1", "Pro forma nr PROF 700/2026", "CLIENT", tt.paid, tt.paidCurrency),
			}

			if _, err := testMatcher(db).MatchBankPayments(context.Background()); err != nil {
				t.Fatal(err)
			}
			fact := db.facts["order-1"]
			if fact == nil {
				t.Fatal("no payment fact recorded")
			}
			if fact.AmountStatus != tt.wantAmount {
				t.Errorf("amount_status = %q, want %q", fact.AmountStatus, tt.wantAmount)
			}
			if fact.Status != tt.wantStatus {
				t.Errorf("status = %q, want %q", fact.Status, tt.wantStatus)
			}
			if got := fact.Settled(); got != tt.wantSettleable {
				t.Errorf("Settled() = %v, want %v", got, tt.wantSettleable)
			}
		})
	}
}

// TestPartialPaymentsAccumulate covers an order settled by two transfers: the second must
// add to the first, not replace it.
func TestPartialPaymentsAccumulate(t *testing.T) {
	db := newFakeMatchDB()
	db.byNumber["PROF 700/2026"] = doc("PROF 700/2026", "order-1", "PLN", 1000.00)
	c := testMatcher(db)

	db.unmatched = []*entity.BankTransaction{credit("k1", "PROF 700/2026", "CLIENT", 40000, "PLN")}
	if _, err := c.MatchBankPayments(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := db.facts["order-1"]; got.AmountPaid != 40000 || got.Status != entity.PaymentFactPartial {
		t.Fatalf("after first: paid=%d status=%s", got.AmountPaid, got.Status)
	}

	db.unmatched = []*entity.BankTransaction{credit("k2", "PROF 700/2026", "CLIENT", 60000, "PLN")}
	if _, err := c.MatchBankPayments(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := db.facts["order-1"]
	if got.AmountPaid != 100000 {
		t.Errorf("paid = %d, want 100000", got.AmountPaid)
	}
	if got.Status != entity.PaymentFactPaid {
		t.Errorf("status = %q, want paid", got.Status)
	}
	if len(got.TransactionKeys) != 2 {
		t.Errorf("transaction keys = %v, want 2", got.TransactionKeys)
	}
}

// TestSameTransactionIsNotCountedTwice guards against a re-run inflating the paid total:
// the sliding window re-delivers entries, and matching is triggered after every poll.
func TestSameTransactionIsNotCountedTwice(t *testing.T) {
	db := newFakeMatchDB()
	db.byNumber["PROF 700/2026"] = doc("PROF 700/2026", "order-1", "PLN", 1000.00)
	tx := credit("k1", "PROF 700/2026", "CLIENT", 100000, "PLN")
	c := testMatcher(db)

	db.unmatched = []*entity.BankTransaction{tx}
	if _, err := c.MatchBankPayments(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Same entry offered again, as a repeated pass would.
	db.unmatched = []*entity.BankTransaction{tx}
	if _, err := c.MatchBankPayments(context.Background()); err != nil {
		t.Fatal(err)
	}

	if got := db.facts["order-1"].AmountPaid; got != 100000 {
		t.Errorf("paid = %d, want 100000 — the same transfer was counted twice", got)
	}
}

// TestManualMatch covers resolving a queued payment by hand.
func TestManualMatch(t *testing.T) {
	db := newFakeMatchDB()
	db.byNumber["PROF 700/2026"] = doc("PROF 700/2026", "order-1", "PLN", 157.00)
	db.unmatched = []*entity.BankTransaction{credit("k1", "anne materijal", "ANNE STUDIO", 15700, "PLN")}

	c := testMatcher(db)
	if err := c.MatchBankPaymentManually(context.Background(), "k1", "PROF 700/2026"); err != nil {
		t.Fatal(err)
	}

	fact := db.facts["order-1"]
	if fact == nil || fact.MatchLevel != entity.MatchManual {
		t.Fatalf("fact = %+v, want a manual match", fact)
	}
	if db.marks["k1"] != entity.BankMatchMatched {
		t.Errorf("mark = %q, want matched", db.marks["k1"])
	}
}

func TestParseValidUntil(t *testing.T) {
	tests := []struct {
		in   string
		zero bool
	}{
		{"2027-05-17T08:12:00.000000+00:00", false},
		{"2027-05-17T08:12:00Z", false},
		{"2027-05-17", false},
		{"", true},
		{"garbage", true},
	}
	for _, tt := range tests {
		if got := parseValidUntil(tt.in); got.IsZero() != tt.zero {
			t.Errorf("parseValidUntil(%q) = %v, zero want %v", tt.in, got, tt.zero)
		}
	}
}

// TestMatchBareNumber covers references that quote the number with no FV/PROF prefix,
// taken from a real November 2026 statement. A bare number does not say whether it is a
// proforma or an invoice, so only the currency and exact amount may confirm it.
func TestMatchBareNumber(t *testing.T) {
	tests := []struct {
		name       string
		remittance string
		amount     int64
		wantDoc    string // empty: must stay unmatched
	}{
		{"slashed", "1137/2026 FOREIGN-TRANSFER-IN", 147955, "PROF 1137/2026"},
		{"with a word in front", "ANNE 1038/2026 INSTANT-SEPA-TRANSFER", 147955, "PROF 1038/2026"},
		{"space instead of slash", "1100 2026 INSTANT-SEPA-TRANSFER", 147955, "PROF 1100/2026"},
		{"order word, space", "ORDER 1101 2026 INSTANT-SEPA-TRANSFER", 147955, "PROF 1101/2026"},
		{"run together", "11452026 INSTANT-SEPA-TRANSFER", 147955, "PROF 1145/2026"},
		{"spelled-out prefix", "FAKT.PRO FORMA 1091/2026 TRANSFER-IN", 50000, "PROF 1091/2026"},
		{"amount differs", "1137/2026 FOREIGN-TRANSFER-IN", 147900, ""},
		{"no year", "INVOICE 782 INSTANT-SEPA-TRANSFER", 147955, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newFakeMatchDB()
			for _, n := range []string{"PROF 1137/2026", "PROF 1038/2026", "PROF 1100/2026", "PROF 1101/2026", "PROF 1145/2026"} {
				db.byNumber[n] = doc(n, "order-"+n, "EUR", 1479.55)
			}
			// An invoice with the same number in another currency must not compete.
			db.byNumber["FV 1137/2026"] = doc("FV 1137/2026", "order-fv", "PLN", 1479.55)
			db.byNumber["PROF 1091/2026"] = doc("PROF 1091/2026", "order-PROF 1091/2026", "EUR", 500.00)
			db.unmatched = []*entity.BankTransaction{credit("k1", tt.remittance, "CLIENT", tt.amount, "EUR")}

			res, err := testMatcher(db).MatchBankPayments(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if tt.wantDoc == "" {
				if res.Matched != 0 {
					t.Fatalf("matched %d, want 0", res.Matched)
				}
				return
			}
			if res.Matched != 1 {
				t.Fatalf("matched %d, want 1", res.Matched)
			}
			if got := db.facts["order-"+tt.wantDoc]; got == nil || got.Status != entity.PaymentFactPaid {
				t.Errorf("fact for %s = %+v, want paid", tt.wantDoc, got)
			}
		})
	}
}

// TestBareNumberAmbiguous leaves a payment for a person when a proforma and an invoice
// share the number, currency and total.
func TestBareNumberAmbiguous(t *testing.T) {
	db := newFakeMatchDB()
	db.byNumber["PROF 1137/2026"] = doc("PROF 1137/2026", "order-1", "EUR", 100.00)
	db.byNumber["FV 1137/2026"] = doc("FV 1137/2026", "order-2", "EUR", 100.00)
	db.unmatched = []*entity.BankTransaction{credit("k1", "1137/2026", "CLIENT", 10000, "EUR")}

	res, err := testMatcher(db).MatchBankPayments(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Matched != 0 {
		t.Errorf("matched %d, want 0", res.Matched)
	}
}

// TestOrderNumberOutranksBareNumber guards an order number whose digits also read as
// "number + year".
func TestOrderNumberOutranksBareNumber(t *testing.T) {
	db := newFakeMatchDB()
	db.byRef["12026"] = doc("FV 900/2026", "12026", "PLN", 100.00)
	db.byNumber["PROF 1/2026"] = doc("PROF 1/2026", "order-other", "PLN", 100.00)
	db.unmatched = []*entity.BankTransaction{credit("k1", "Numer zamówienia: 12026", "CLIENT", 10000, "PLN")}

	if _, err := testMatcher(db).MatchBankPayments(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := db.facts["12026"]; !ok {
		t.Error("order number was not used")
	}
	if _, ok := db.facts["order-other"]; ok {
		t.Error("bare number reading won over the stated order number")
	}
}

// TestSplitAcrossDocuments covers one transfer paying several proformas, each quoted by
// number: every document is settled with its own total.
func TestSplitAcrossDocuments(t *testing.T) {
	const remit = "ProfiVN shop s.r.o. 1103/2026, 1104/2026, 1097/2026 FOREIGN-TRANSFER-IN"
	setup := func() *fakeMatchDB {
		db := newFakeMatchDB()
		db.byNumber["PROF 1103/2026"] = doc("PROF 1103/2026", "o-1103", "EUR", 1000.00)
		db.byNumber["PROF 1104/2026"] = doc("PROF 1104/2026", "o-1104", "EUR", 250.50)
		db.byNumber["PROF 1097/2026"] = doc("PROF 1097/2026", "o-1097", "EUR", 49.50)
		return db
	}

	t.Run("totals add up", func(t *testing.T) {
		db := setup()
		db.unmatched = []*entity.BankTransaction{credit("k1", remit, "PROFIVN SHOP", 130000, "EUR")}
		res, err := testMatcher(db).MatchBankPayments(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if res.Matched != 1 {
			t.Fatalf("matched %d, want 1", res.Matched)
		}
		for ref, want := range map[string]int64{"o-1103": 100000, "o-1104": 25050, "o-1097": 4950} {
			f := db.facts[ref]
			if f == nil || f.AmountPaid != want || f.Status != entity.PaymentFactPaid {
				t.Errorf("%s: fact = %+v, want paid %d", ref, f, want)
			}
		}
		if db.marks["k1"] != entity.BankMatchMatched {
			t.Errorf("mark = %q, want matched", db.marks["k1"])
		}
	})

	t.Run("totals differ", func(t *testing.T) {
		db := setup()
		db.unmatched = []*entity.BankTransaction{credit("k1", remit, "PROFIVN SHOP", 129999, "EUR")}
		res, err := testMatcher(db).MatchBankPayments(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if res.Matched != 0 || len(db.facts) != 0 {
			t.Errorf("matched %d, facts %d, want none", res.Matched, len(db.facts))
		}
	})

	t.Run("one document missing", func(t *testing.T) {
		db := setup()
		delete(db.byNumber, "PROF 1097/2026")
		db.unmatched = []*entity.BankTransaction{credit("k1", remit, "PROFIVN SHOP", 125050, "EUR")}
		res, err := testMatcher(db).MatchBankPayments(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if res.Matched != 0 {
			t.Errorf("matched %d, want 0", res.Matched)
		}
	})
}
