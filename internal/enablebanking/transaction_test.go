package enablebanking

import (
	"testing"

	"wfsync/entity"
)

func TestParseMinorUnits(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int64
	}{
		{"integer with trailing zero", "88.0", 8800},
		{"two decimals", "1234.56", 123456},
		{"no decimals", "500", 50000},
		{"one decimal", "0.5", 50},
		{"three decimals round up", "10.005", 1001},
		{"three decimals round down", "10.004", 1000},
		{"negative is stored unsigned", "-741.27", 74127},
		{"whitespace tolerated", " 362.78 ", 36278},
		{"empty", "", 0},
		{"garbage", "n/a", 0},
		// A value float64 cannot hold exactly; parsing through it would give 267349.
		{"exact decimal", "2673.50", 267350},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseMinorUnits(tt.in); got != tt.want {
				t.Errorf("parseMinorUnits(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestNormalizeRemittance(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "Pro forma nr PROF 765/2026", "PROFORMANRPROF765/2026"},
		// PKO wraps fields at a fixed width and joins the pieces with a space, so a
		// document number can be split at any position.
		{"number split by wrap", "Pro forma nr PROF 74 5/2026", "PROFORMANRPROF745/2026"},
		{"swift reference", "/REF/281475550255106/PROF 740/2026 . DARK", "/REF/281475550255106/PROF740/2026.DARK"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := entity.NormalizeRemittance(tt.in); got != tt.want {
				t.Errorf("NormalizeRemittance(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestToBankTransaction(t *testing.T) {
	const ourIBAN = "PL61109010140000071219812874"

	tests := []struct {
		name          string
		tx            Transaction
		wantDirection string
		wantAmount    int64
		wantParty     string
		wantPartyIBAN string
		wantRemitKey  string
	}{
		{
			name: "credit takes the debtor as counterparty",
			tx: Transaction{
				EntryReference:        "67430598670055240",
				TransactionAmount:     Amount{Currency: "pln", Amount: "362.78"},
				CreditDebitIndicator:  "CRDT",
				Status:                "BOOK",
				BookingDate:           "2026-08-31",
				Debtor:                &Party{Name: "TARAS TOKARYK"},
				DebtorAccount:         &PartyAccount{IBAN: "PL84109025290000000152636617"},
				Creditor:              &Party{Name: "should be ignored"},
				RemittanceInformation: []string{"Faktura nr FV 768/2026"},
			},
			wantDirection: entity.DirectionCredit,
			wantAmount:    36278,
			wantParty:     "TARAS TOKARYK",
			wantPartyIBAN: "PL84109025290000000152636617",
			wantRemitKey:  "FAKTURANRFV768/2026",
		},
		{
			name: "debit takes the creditor as counterparty",
			tx: Transaction{
				TransactionAmount:     Amount{Currency: "PLN", Amount: "741.27"},
				CreditDebitIndicator:  "DBIT",
				Status:                "BOOK",
				BookingDate:           "2026-08-31",
				Creditor:              &Party{Name: "E.ON POLSKA S.A."},
				CreditorAccount:       &PartyAccount{IBAN: "PL97124069601701082500072669"},
				Debtor:                &Party{Name: "should be ignored"},
				RemittanceInformation: []string{"FAKTURY NR 245250235760"},
			},
			wantDirection: entity.DirectionDebit,
			wantAmount:    74127,
			wantParty:     "E.ON POLSKA S.A.",
			wantPartyIBAN: "PL97124069601701082500072669",
			wantRemitKey:  "FAKTURYNR245250235760",
		},
		{
			// A document number straddling two lines must survive the join.
			name: "remittance lines are joined",
			tx: Transaction{
				TransactionAmount:     Amount{Currency: "PLN", Amount: "2485.03"},
				CreditDebitIndicator:  "CRDT",
				Status:                "BOOK",
				BookingDate:           "2026-08-20",
				RemittanceInformation: []string{"/REF/281475550255106/PROF 740", "/2026 . DARK NAIL GELS"},
			},
			wantDirection: entity.DirectionCredit,
			wantAmount:    248503,
			wantRemitKey:  "/REF/281475550255106/PROF740/2026.DARKNAILGELS",
		},
		{
			// Falls back to the non-IBAN identifier when the bank sends no IBAN.
			name: "counterparty without iban",
			tx: Transaction{
				TransactionAmount:    Amount{Currency: "PLN", Amount: "88"},
				CreditDebitIndicator: "CRDT",
				Status:               "BOOK",
				BookingDate:          "2026-08-01",
				DebtorAccount:        &PartyAccount{Other: &AccountRef{Identification: "2345533245", SchemeName: "CPAN"}},
			},
			wantDirection: entity.DirectionCredit,
			wantAmount:    8800,
			wantPartyIBAN: "2345533245",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ToBankTransaction(&tt.tx, ourIBAN)

			if got.AccountIBAN != ourIBAN {
				t.Errorf("AccountIBAN = %q, want %q", got.AccountIBAN, ourIBAN)
			}
			if got.Direction != tt.wantDirection {
				t.Errorf("Direction = %q, want %q", got.Direction, tt.wantDirection)
			}
			if got.Amount != tt.wantAmount {
				t.Errorf("Amount = %d, want %d", got.Amount, tt.wantAmount)
			}
			if got.Currency != "PLN" {
				t.Errorf("Currency = %q, want PLN", got.Currency)
			}
			if got.PartyName != tt.wantParty {
				t.Errorf("PartyName = %q, want %q", got.PartyName, tt.wantParty)
			}
			if got.PartyIBAN != tt.wantPartyIBAN {
				t.Errorf("PartyIBAN = %q, want %q", got.PartyIBAN, tt.wantPartyIBAN)
			}
			if got.RemittanceKey != tt.wantRemitKey {
				t.Errorf("RemittanceKey = %q, want %q", got.RemittanceKey, tt.wantRemitKey)
			}
			if got.Source != entity.BankSourceEnableBanking {
				t.Errorf("Source = %q, want %q", got.Source, entity.BankSourceEnableBanking)
			}
			if got.Key == "" {
				t.Error("Key is empty")
			}
		})
	}
}

// TestKeySeparatesFeeFromTransfer guards the reason the idempotency key is a hash
// rather than the bank's own reference: in a real PKO statement the fee charged for a
// transfer carries the same transaction identifier as the transfer itself, and keying
// on that identifier alone would collapse the two entries into one.
func TestKeySeparatesFeeFromTransfer(t *testing.T) {
	const sharedRef = "67430502600162983"

	transfer := ToBankTransaction(&Transaction{
		EntryReference:        sharedRef,
		TransactionAmount:     Amount{Currency: "PLN", Amount: "5822.00"},
		CreditDebitIndicator:  "DBIT",
		BookingDate:           "2026-08-31",
		RemittanceInformation: []string{"FAKTURY: 1-27.08.26"},
	}, "PL61109010140000071219812874")

	fee := ToBankTransaction(&Transaction{
		EntryReference:        sharedRef,
		TransactionAmount:     Amount{Currency: "PLN", Amount: "10.00"},
		CreditDebitIndicator:  "DBIT",
		BookingDate:           "2026-08-31",
		RemittanceInformation: []string{"OPŁATA - PRZELEW NATYCH. WYCH."},
	}, "PL61109010140000071219812874")

	if transfer.Key == fee.Key {
		t.Fatalf("transfer and its fee share key %s; they must be stored as two entries", transfer.Key)
	}
}

// TestKeyIsStableAcrossReads ensures re-reading the same entry in the sliding window
// yields the same key, so a poll does not duplicate rows already stored.
func TestKeyIsStableAcrossReads(t *testing.T) {
	tx := Transaction{
		EntryReference:        "67430598670055240",
		TransactionAmount:     Amount{Currency: "PLN", Amount: "362.78"},
		CreditDebitIndicator:  "CRDT",
		BookingDate:           "2026-08-31",
		Debtor:                &Party{Name: "TARAS TOKARYK"},
		DebtorAccount:         &PartyAccount{IBAN: "PL84109025290000000152636617"},
		RemittanceInformation: []string{"Faktura nr FV 768/2026"},
	}

	first := ToBankTransaction(&tx, "PL61109010140000071219812874")
	second := ToBankTransaction(&tx, "PL61109010140000071219812874")

	if first.Key != second.Key {
		t.Errorf("key not stable: %s vs %s", first.Key, second.Key)
	}
}

// TestAssignKeysSeparatesIdenticalEntries covers a statement holding two entries the
// bank reports identically — same reference, amount, date and description. They are
// two payments, and must survive as two rows; the sample statement published by
// Enable Banking contains exactly such a pair.
func TestAssignKeysSeparatesIdenticalEntries(t *testing.T) {
	const iban = "DK0550517826136334"
	raw := Transaction{
		EntryReference:        "3845245274",
		TransactionAmount:     Amount{Currency: "DKK", Amount: "45.46"},
		CreditDebitIndicator:  "CRDT",
		Status:                "BOOK",
		BookingDate:           "2021-06-25",
		RemittanceInformation: []string{"WeShare: Netværk"},
	}

	first := ToBankTransaction(&raw, iban)
	second := ToBankTransaction(&raw, iban)
	batch := []*entity.BankTransaction{first, second}
	entity.AssignKeys(batch)

	if first.Key == second.Key {
		t.Fatalf("identical entries collapsed into one key %s", first.Key)
	}

	// A re-read of the same window must reproduce both keys, or every poll would
	// insert the pair again.
	reread := []*entity.BankTransaction{ToBankTransaction(&raw, iban), ToBankTransaction(&raw, iban)}
	entity.AssignKeys(reread)
	if reread[0].Key != first.Key || reread[1].Key != second.Key {
		t.Error("keys are not reproducible across reads of the same window")
	}

	// A third identical entry booked later appends; it must not renumber the first two.
	grown := []*entity.BankTransaction{
		ToBankTransaction(&raw, iban), ToBankTransaction(&raw, iban), ToBankTransaction(&raw, iban),
	}
	entity.AssignKeys(grown)
	if grown[0].Key != first.Key || grown[1].Key != second.Key {
		t.Error("a newly booked identical entry renumbered the existing ones")
	}
}
