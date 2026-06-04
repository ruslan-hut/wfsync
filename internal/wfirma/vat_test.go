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
