package vies

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
	"wfsync/entity"
)

// newTestService points the service at a fake VIES and shrinks the backoff so the
// retry path costs no real wall-clock time.
func newTestService(t *testing.T, handler http.HandlerFunc) *Service {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	return &Service{
		hc:         srv.Client(),
		log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		attempts:   3,
		retryDelay: time.Millisecond,
		baseURL:    srv.URL + "/ms/%s/vat/%s",
	}
}

// throttleThen answers with MS_MAX_CONCURRENT_REQ for the first n calls and with body
// afterwards, mimicking a member state that rate-limits a burst and then answers.
func throttleThen(calls *int32, n int32, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if atomic.AddInt32(calls, 1) <= n {
			_, _ = io.WriteString(w, `{"isValid":false,"userError":"MS_MAX_CONCURRENT_REQ"}`)
			return
		}
		_, _ = io.WriteString(w, body)
	}
}

// The case from production: France throttles the first call, so a single-shot check
// would report inconclusive and cost the customer their 0% WDT rate.
func TestRetriesThroughMemberStateThrottle(t *testing.T) {
	var calls int32
	s := newTestService(t, throttleThen(&calls, 1, `{"isValid":true,"userError":"VALID","name":"BELGRAVIA"}`))

	if got := s.ValidateTaxId("FR24922403365", "FR"); got != entity.VIESValid {
		t.Errorf("result = %v, want VIESValid", got)
	}
	if calls != 2 {
		t.Errorf("VIES calls = %d, want 2 (one throttled, one answered)", calls)
	}
}

// The budget is finite: a member state throttling every attempt still ends inconclusive
// rather than looping, and never reports the number as invalid.
func TestGivesUpAfterAttemptsExhausted(t *testing.T) {
	var calls int32
	s := newTestService(t, throttleThen(&calls, 100, ""))

	if got := s.ValidateTaxId("FR24922403365", "FR"); got != entity.VIESInconclusive {
		t.Errorf("result = %v, want VIESInconclusive", got)
	}
	if calls != 3 {
		t.Errorf("VIES calls = %d, want 3 (the attempt budget)", calls)
	}
}

// A definitive verdict must not be retried — an invalid number is an answer, not a
// transient failure, and re-asking would only add load and latency.
func TestDefinitiveVerdictIsNotRetried(t *testing.T) {
	var calls int32
	s := newTestService(t, func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		_, _ = io.WriteString(w, `{"isValid":false,"userError":"INVALID"}`)
	})

	if got := s.ValidateTaxId("FR24922403365", "FR"); got != entity.VIESInvalid {
		t.Errorf("result = %v, want VIESInvalid", got)
	}
	if calls != 1 {
		t.Errorf("VIES calls = %d, want 1", calls)
	}
}

// A transport-level failure (VIES unreachable, 5xx) is retried on the same budget.
func TestRetriesTransportFailure(t *testing.T) {
	var calls int32
	s := newTestService(t, func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			http.Error(w, "gateway timeout", http.StatusGatewayTimeout)
			return
		}
		_, _ = io.WriteString(w, `{"isValid":true,"userError":"VALID"}`)
	})

	if got := s.ValidateTaxId("FR24922403365", "FR"); got != entity.VIESValid {
		t.Errorf("result = %v, want VIESValid", got)
	}
	if calls != 2 {
		t.Errorf("VIES calls = %d, want 2", calls)
	}
}

// A throttled live check falls back to a cached definitive verdict rather than losing
// the B2B status of a customer we have already validated once.
func TestThrottleFallsBackToStaleCache(t *testing.T) {
	var calls int32
	s := newTestService(t, throttleThen(&calls, 100, ""))
	s.cacheAge = time.Hour
	s.db = &stubDB{stored: &entity.VIESValidation{
		CountryCode: "FR", VATNumber: "24922403365", Valid: true,
		ValidatedAt: time.Now().Add(-48 * time.Hour), // stale
	}}

	if got := s.ValidateTaxId("FR24922403365", "FR"); got != entity.VIESValid {
		t.Errorf("result = %v, want VIESValid from stale cache", got)
	}
}

// stubDB serves one stored validation and records saves.
type stubDB struct {
	stored *entity.VIESValidation
	saved  *entity.VIESValidation
}

func (d *stubDB) SaveVIESValidation(v *entity.VIESValidation) error {
	d.saved = v
	return nil
}

func (d *stubDB) GetVIESValidation(countryCode, vatNumber string) (*entity.VIESValidation, error) {
	if d.stored != nil && d.stored.CountryCode == countryCode && d.stored.VATNumber == vatNumber {
		return d.stored, nil
	}
	return nil, nil
}
