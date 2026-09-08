package enablebanking

import (
	"encoding/json"
	"math/big"
	"strings"

	"wfsync/entity"
)

// ToBankTransaction converts one Enable Banking entry into the source-agnostic model
// the payment matcher consumes. accountIBAN identifies the account being read, which
// the entry itself does not carry.
//
// The counterparty is picked by direction: on money in the other side is the debtor
// (who paid us), on money out it is the creditor. A bank's own CSV export collapses
// both into a single "counterparty" column, so this is where the two shapes converge.
func ToBankTransaction(t *Transaction, accountIBAN string) *entity.BankTransaction {
	bt := &entity.BankTransaction{
		AccountIBAN: accountIBAN,
		Direction:   strings.ToUpper(t.CreditDebitIndicator),
		Amount:      parseMinorUnits(t.TransactionAmount.Amount),
		Currency:    strings.ToUpper(t.TransactionAmount.Currency),
		BookingDate: t.BookingDate,
		ValueDate:   t.ValueDate,
		EntryRef:    t.EntryReference,
		BankCode:    bankCode(t.BankTransactionCode),
		Source:      entity.BankSourceEnableBanking,
	}

	party, account := t.Debtor, t.DebtorAccount
	if bt.Direction == entity.DirectionDebit {
		party, account = t.Creditor, t.CreditorAccount
	}
	if party != nil {
		bt.PartyName = strings.TrimSpace(party.Name)
	}
	if account != nil {
		bt.PartyIBAN = strings.TrimSpace(account.IBAN)
		if bt.PartyIBAN == "" && account.Other != nil {
			bt.PartyIBAN = strings.TrimSpace(account.Other.Identification)
		}
	}

	bt.SetRemittance(joinRemittance(t.RemittanceInformation))
	if raw, err := json.Marshal(t); err == nil {
		bt.Raw = string(raw)
	}
	bt.BuildKey()
	return bt
}

// joinRemittance flattens the payment reference lines into one string. Enable Banking
// splits a reference into as many lines as the bank sent, and a document number can
// straddle two of them, so the pieces are joined with a single space and normalized
// away entirely by entity.NormalizeRemittance before matching.
func joinRemittance(lines []string) string {
	parts := make([]string, 0, len(lines))
	for _, l := range lines {
		if l = strings.TrimSpace(l); l != "" {
			parts = append(parts, l)
		}
	}
	return strings.Join(parts, " ")
}

// bankCode flattens the bank's classification into "code/sub_code".
func bankCode(c BankTransactionCode) string {
	switch {
	case c.Code != "" && c.SubCode != "":
		return c.Code + "/" + c.SubCode
	case c.Code != "":
		return c.Code
	default:
		return c.SubCode
	}
}

// parseMinorUnits converts a decimal amount string into minor units.
//
// The value arrives as text ("88.0", "1234.56") and is money, so it is parsed exactly
// with big.Rat rather than through float64, which cannot represent every two-decimal
// value and would round a cent off large invoices. A value that will not parse yields
// 0, which the matcher treats as unusable rather than as a free match.
func parseMinorUnits(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return 0
	}
	// Scale to minor units, then round half away from zero.
	r.Mul(r, big.NewRat(100, 1))
	num, den := r.Num(), r.Denom()
	q, rem := new(big.Int).QuoRem(num, den, new(big.Int))
	rem.Abs(rem)
	rem.Mul(rem, big.NewInt(2))
	if rem.Cmp(den) >= 0 {
		if r.Sign() < 0 {
			q.Sub(q, big.NewInt(1))
		} else {
			q.Add(q, big.NewInt(1))
		}
	}
	if q.Sign() < 0 {
		q.Neg(q)
	}
	return q.Int64()
}
