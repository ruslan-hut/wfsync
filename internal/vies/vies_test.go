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

// A look-alike Cyrillic letter (PL-666) is a bad number, not a VIES outage, and is
// decided without calling VIES — which would answer HTTP 400.
func TestMalformedTaxIdIsInvalidWithoutCall(t *testing.T) {
	var calls int32
	s := newTestService(t, func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
	})

	if got := s.ValidateTaxId("\u0412E1034615262", "BE"); got != entity.VIESInvalid {
		t.Errorf("result = %v, want VIESInvalid", got)
	}
	if calls != 0 {
		t.Errorf("VIES calls = %d, want 0", calls)
	}
}

// Which numbers reach VIES, and under which country/number. Non-EU prefixes (the Swiss
// UID from .com order 18463, a UK number) and non-EU fallback countries are decided
// without a call; separators are stripped before the number goes into the URL.
func TestValidateTaxIdRouting(t *testing.T) {
	cases := []struct {
		name     string
		taxId    string
		fallback string
		want     entity.VIESResult
		wantPath string // "" = VIES must not be called
	}{
		{"Swiss UID on a Hungarian address", "CHE-266.473.703", "HU", entity.VIESNotEU, ""},
		{"UK VAT number", "GB 123 4567 89", "GB", entity.VIESNotEU, ""},
		{"Norwegian number", "NO123456789MVA", "NO", entity.VIESNotEU, ""},
		{"bare number, non-EU address", "123456789", "US", entity.VIESNotEU, ""},
		{"DE with dots and spaces", " de 123.456 789 ", "DE", entity.VIESValid, "/ms/DE/vat/123456789"},
		{"PL without prefix uses the address", "525-286-34-76", "PL", entity.VIESValid, "/ms/PL/vat/5252863476"},
		{"Greek address maps to EL", "123456789", "GR", entity.VIESValid, "/ms/EL/vat/123456789"},
		{"Northern Ireland XI", "XI123456789", "GB", entity.VIESValid, "/ms/XI/vat/123456789"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls int32
			var path string
			s := newTestService(t, func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&calls, 1)
				path = r.URL.Path
				_, _ = io.WriteString(w, `{"isValid":true,"userError":"VALID"}`)
			})

			if got := s.ValidateTaxId(tc.taxId, tc.fallback); got != tc.want {
				t.Errorf("result = %v, want %v", got, tc.want)
			}
			switch {
			case tc.wantPath == "" && calls != 0:
				t.Errorf("VIES calls = %d, want 0", calls)
			case tc.wantPath != "" && (calls != 1 || path != tc.wantPath):
				t.Errorf("VIES calls = %d path %q, want 1 call to %q", calls, path, tc.wantPath)
			}
		})
	}
}

// HTTP errors: a format rejection is final (invalid, one call), unavailability is
// retried on the attempt budget and ends inconclusive.
func TestHTTPErrorClassification(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		body      string
		want      entity.VIESResult
		wantCalls int32
	}{
		{"400 VOW-ERR-2600 is invalid without retries", http.StatusBadRequest,
			`{"actionSucceed":false,"errorWrappers":[{"error":"VOW-ERR-2600","message":"The input data is invalid"}]}`,
			entity.VIESInvalid, 1},
		{"400 without a body is invalid without retries", http.StatusBadRequest, ``, entity.VIESInvalid, 1},
		{"400 carrying MS_UNAVAILABLE is retried", http.StatusBadRequest,
			`{"actionSucceed":false,"errorWrappers":[{"error":"MS_UNAVAILABLE"}]}`, entity.VIESInconclusive, 3},
		{"429 is retried", http.StatusTooManyRequests, ``, entity.VIESInconclusive, 3},
		{"503 is retried", http.StatusServiceUnavailable, ``, entity.VIESInconclusive, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls int32
			s := newTestService(t, func(w http.ResponseWriter, _ *http.Request) {
				atomic.AddInt32(&calls, 1)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			})
			db := &stubDB{}
			s.db = db

			if got := s.ValidateTaxId("HU12345678", "HU"); got != tc.want {
				t.Errorf("result = %v, want %v", got, tc.want)
			}
			if calls != tc.wantCalls {
				t.Errorf("VIES calls = %d, want %d", calls, tc.wantCalls)
			}
			if db.saved != nil {
				t.Errorf("result cached: %+v", db.saved)
			}
		})
	}
}
