package vatrates

import (
	"io"
	"log/slog"
	"testing"
	"wfsync/entity"
)

// newTestService builds a Service with a discarding logger, sufficient for
// exercising reconcile() which only depends on the logger.
func newTestService() *Service {
	return &Service{
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		rates: make(map[string]float64),
	}
}

// TestReconcileCorrectsStaleRate is the regression test for the incident: a cached
// source reporting Slovakia at the old 20% must be corrected to the baseline 23%.
func TestReconcileCorrectsStaleRate(t *testing.T) {
	s := newTestService()

	out, issues := s.reconcile(map[string]float64{"SK": 20})

	if got := out["SK"]; got != 23 {
		t.Fatalf("SK rate = %v, want 23 (baseline override)", got)
	}
	if issues == 0 {
		t.Fatalf("expected the drift to be flagged, got issues = 0")
	}
}

// TestReconcileFillsMissingCountries ensures every baseline country is present even
// when the source omits it.
func TestReconcileFillsMissingCountries(t *testing.T) {
	s := newTestService()

	out, issues := s.reconcile(map[string]float64{}) // source returned nothing useful

	if len(out) != len(entity.StandardVATRates) {
		t.Fatalf("reconciled %d countries, want %d", len(out), len(entity.StandardVATRates))
	}
	for code, ref := range entity.StandardVATRates {
		if out[code] != ref {
			t.Errorf("country %s = %v, want baseline %v", code, out[code], ref)
		}
	}
	if issues != len(entity.StandardVATRates) {
		t.Errorf("issues = %d, want %d (every country missing)", issues, len(entity.StandardVATRates))
	}
}

// TestReconcileMatchingRatesNoCorrection verifies that correct source data passes
// through untouched and unflagged.
func TestReconcileMatchingRatesNoCorrection(t *testing.T) {
	s := newTestService()

	src := make(map[string]float64, len(entity.StandardVATRates))
	for code, ref := range entity.StandardVATRates {
		src[code] = ref
	}

	out, issues := s.reconcile(src)

	if issues != 0 {
		t.Fatalf("issues = %d, want 0 for exact-match source", issues)
	}
	if len(out) != len(entity.StandardVATRates) {
		t.Fatalf("reconciled %d countries, want %d", len(out), len(entity.StandardVATRates))
	}
}

// TestReconcileUnknownCountryPlausibility checks that a country absent from the
// baseline is kept when its rate is plausible and dropped when it is not.
func TestReconcileUnknownCountryPlausibility(t *testing.T) {
	s := newTestService()

	out, issues := s.reconcile(map[string]float64{
		"ZZ": 21,  // plausible unknown country → kept
		"QQ": 200, // implausible → dropped
		"WW": 3,   // below floor → dropped
	})

	if got, ok := out["ZZ"]; !ok || got != 21 {
		t.Errorf("ZZ = %v (present=%v), want 21 kept", got, ok)
	}
	if _, ok := out["QQ"]; ok {
		t.Errorf("QQ should have been dropped as out of range")
	}
	if _, ok := out["WW"]; ok {
		t.Errorf("WW should have been dropped as below the floor")
	}
	if issues < 2 {
		t.Errorf("issues = %d, want at least 2 drops flagged", issues)
	}
}
