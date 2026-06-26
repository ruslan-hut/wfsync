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

// TestNormalizeEUVatNumber is the regression for the PL-290 incident: a Czech B2B
// buyer's bare national number must gain its "CZ" prefix so wFirma accepts the
// 0% WDT invoice, while already-prefixed, non-EU, and domestic numbers are left
// untouched.
func TestNormalizeEUVatNumber(t *testing.T) {
	cases := []struct {
		name    string
		country string
		taxId   string
		want    string
	}{
		{"bare CZ number gets prefix", "CZ", "28982711", "CZ28982711"},
		{"already prefixed left as-is", "CZ", "CZ28982711", "CZ28982711"},
		{"whitespace trimmed then prefixed", "CZ", "  28982711 ", "CZ28982711"},
		{"Greece uses EL prefix", "GR", "123456789", "EL123456789"},
		{"Greek EL already present", "GR", "EL123456789", "EL123456789"},
		{"Polish NIP untouched (not in EU map)", "PL", "1234567890", "1234567890"},
		{"non-EU country untouched", "US", "123456789", "123456789"},
		{"empty tax id untouched", "CZ", "", ""},
		{"empty country untouched", "", "28982711", "28982711"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeEUVatNumber(tc.country, tc.taxId); got != tc.want {
				t.Errorf("normalizeEUVatNumber(%q, %q) = %q, want %q", tc.country, tc.taxId, got, tc.want)
			}
		})
	}
}
