package wfirma

import (
	"testing"
	"wfsync/entity"
)

// TestDerivedMapsMatchBaseline guards the refactor that made entity.StandardVATRates
// the single source of truth: the fallback maps must mirror it exactly so compliance
// data can never drift between packages.
func TestDerivedMapsMatchBaseline(t *testing.T) {
	if len(euCountries) != len(entity.StandardVATRates) {
		t.Fatalf("euCountries has %d entries, baseline has %d", len(euCountries), len(entity.StandardVATRates))
	}
	if len(defaultEURates) != len(entity.StandardVATRates) {
		t.Fatalf("defaultEURates has %d entries, baseline has %d", len(defaultEURates), len(entity.StandardVATRates))
	}
	for code, rate := range entity.StandardVATRates {
		if !euCountries[code] {
			t.Errorf("euCountries missing %s", code)
		}
		if defaultEURates[code] != int(rate) {
			t.Errorf("defaultEURates[%s] = %d, want %d", code, defaultEURates[code], int(rate))
		}
	}
	// Poland is handled separately and must never appear in the EU fallback maps.
	if euCountries["PL"] {
		t.Errorf("euCountries must not contain PL")
	}
}

// TestSlovakiaB2CRate is the end-to-end regression for the incident: a Slovak B2C
// order without an explicit rate must resolve to the current 23%, not the old 20%.
func TestSlovakiaB2CRate(t *testing.T) {
	// taxRate 0 → fall back to defaultEURates (no dynamic provider supplied).
	if got := resolveGoodsVatCode(0, "SK", false, false, nil); got != "23" {
		t.Fatalf("SK B2C rate = %q, want \"23\"", got)
	}
}

// TestExpectedB2BVATRate locks the numeric rates that B2B order payloads are
// validated against: PL and EU-without-VAT default to 23%, EU-with-VAT is 0% WDT,
// and non-EU is 0% EXP. These are the rates a calling system must agree with or
// its order is rejected with ErrVATRateMismatch.
func TestExpectedB2BVATRate(t *testing.T) {
	cases := []struct {
		name     string
		country  string
		hasTaxId bool
		want     int
	}{
		{"PL with vat number", "PL", true, 23},
		{"PL without vat number", "PL", false, 23},
		{"empty country", "", false, 23},
		{"EU with vat number → WDT 0%", "DE", true, 0},
		{"EU without vat number → 23%", "DE", false, 23},
		{"non-EU with vat number → EXP 0%", "US", true, 0},
		{"non-EU without vat number → EXP 0%", "US", false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// nil provider → fall back to the hardcoded euCountries map.
			if got := ExpectedB2BVATRate(tc.country, tc.hasTaxId, nil); got != tc.want {
				t.Errorf("ExpectedB2BVATRate(%q, %t) = %d, want %d", tc.country, tc.hasTaxId, got, tc.want)
			}
		})
	}
}

// TestResolveTaxId covers both incidents this helper exists for: a Czech B2B buyer's
// bare national number must gain its "CZ" prefix so wFirma accepts the 0% WDT invoice
// (PL-290), and a Polish buyer's number must go out as a NIP rather than as a prefixless
// "custom" identifier, which KSeF rejects with "Pole KodUE posiada niepoprawną wartość"
// (order 17240, whose buyer typed a nine-digit NIP).
func TestResolveTaxId(t *testing.T) {
	// A valid Polish NIP: checksum verified, used wherever the test needs a good one.
	const validNIP = "1234563218"

	cases := []struct {
		name        string
		country     string
		taxId       string
		wantNip     string
		wantType    string
		wantDropped bool
	}{
		{"bare CZ number gets prefix", "CZ", "28982711", "CZ28982711", taxIdTypeCustom, false},
		{"already prefixed left as-is", "CZ", "CZ28982711", "CZ28982711", taxIdTypeCustom, false},
		{"separators stripped then prefixed", "CZ", "  289-827 11 ", "CZ28982711", taxIdTypeCustom, false},
		{"Greece uses EL prefix", "GR", "123456789", "EL123456789", taxIdTypeCustom, false},
		{"Greek EL already present", "GR", "EL123456789", "EL123456789", taxIdTypeCustom, false},
		{"prefix on the number wins over the address country", "DE", "CZ28982711", "CZ28982711", taxIdTypeCustom, false},
		{"valid Polish NIP goes out as a NIP", "PL", validNIP, validNIP, taxIdTypeNip, false},
		{"Polish NIP with separators", "PL", "123-456-32-18", validNIP, taxIdTypeNip, false},
		{"PL-prefixed Polish NIP loses the prefix", "PL", "PL" + validNIP, validNIP, taxIdTypeNip, false},
		{"nine-digit Polish NIP is dropped", "PL", "822855342", "", taxIdTypeNone, true},
		{"Polish NIP failing the checksum is dropped", "PL", "1234567890", "", taxIdTypeNone, true},
		{"non-EU country is dropped", "US", "123456789", "", taxIdTypeNone, true},
		{"no country to derive a prefix from is dropped", "", "28982711", "", taxIdTypeNone, true},
		{"empty tax id is not a failure", "CZ", "", "", taxIdTypeNone, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nip, taxIdType, reason := resolveTaxId(tc.country, tc.taxId)
			if nip != tc.wantNip || taxIdType != tc.wantType {
				t.Errorf("resolveTaxId(%q, %q) = (%q, %q), want (%q, %q)",
					tc.country, tc.taxId, nip, taxIdType, tc.wantNip, tc.wantType)
			}
			if dropped := reason != ""; dropped != tc.wantDropped {
				t.Errorf("resolveTaxId(%q, %q) reason = %q, want dropped=%t",
					tc.country, tc.taxId, reason, tc.wantDropped)
			}
		})
	}
}
