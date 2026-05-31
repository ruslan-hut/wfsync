// Package vies validates EU VAT numbers against the official VIES REST API.
// Results are cached in MongoDB to reduce API calls; stale cache is used as fallback
// when the API is unavailable. Validation is on-demand (no background goroutine).
package vies

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"wfsync/entity"
	"wfsync/internal/config"
	"wfsync/lib/sl"
)

const baseURL = "https://ec.europa.eu/taxation_customs/vies/rest-api/ms/%s/vat/%s"

// Database defines the persistence methods the VIES service needs.
type Database interface {
	SaveVIESValidation(v *entity.VIESValidation) error
	GetVIESValidation(countryCode, vatNumber string) (*entity.VIESValidation, error)
}

// viesResponse is the JSON body returned by the VIES REST API.
//
// userError carries the authoritative verdict: "VALID" / "INVALID" are definitive,
// while any other value (MS_MAX_CONCURRENT_REQ, GLOBAL_MAX_CONCURRENT_REQ,
// MS_UNAVAILABLE, SERVICE_UNAVAILABLE, TIMEOUT, ...) signals the check could not be
// completed. In those cases isValid is false even though the number may be valid, so
// isValid must not be trusted on its own.
type viesResponse struct {
	IsValid           bool   `json:"isValid"`
	RequestDate       string `json:"requestDate"`
	UserError         string `json:"userError"`
	Name              string `json:"name"`
	Address           string `json:"address"`
	RequestIdentifier string `json:"requestIdentifier"`
	VATNumber         string `json:"vatNumber"`
}

// result maps the VIES response to a definitive or inconclusive outcome.
// The userError code is authoritative; isValid is only consulted when no code is present.
func (r *viesResponse) result() entity.VIESResult {
	switch strings.ToUpper(strings.TrimSpace(r.UserError)) {
	case "VALID":
		return entity.VIESValid
	case "INVALID":
		return entity.VIESInvalid
	case "":
		// Edge responses without a userError code: fall back to the boolean.
		if r.IsValid {
			return entity.VIESValid
		}
		return entity.VIESInvalid
	default:
		// MS_MAX_CONCURRENT_REQ, MS_UNAVAILABLE, TIMEOUT, etc. — not a verdict.
		return entity.VIESInconclusive
	}
}

// boolToResult maps a cached boolean verdict to a definitive result.
// Only definitive verdicts are ever cached, so the cache never yields VIESInconclusive.
func boolToResult(valid bool) entity.VIESResult {
	if valid {
		return entity.VIESValid
	}
	return entity.VIESInvalid
}

// Service validates EU VAT numbers via the VIES API with MongoDB caching.
type Service struct {
	hc       *http.Client
	log      *slog.Logger
	db       Database
	cacheAge time.Duration
}

// New creates a VIES validation service.
func New(conf *config.Config, log *slog.Logger) *Service {
	hours := conf.VIES.CacheHours
	if hours <= 0 {
		hours = 720
	}
	return &Service{
		hc:       &http.Client{Timeout: 20 * time.Second},
		log:      log.With(sl.Module("vies")),
		cacheAge: time.Duration(hours) * time.Hour,
	}
}

// SetDatabase configures optional MongoDB persistence for caching validation results.
func (s *Service) SetDatabase(db Database) {
	s.db = db
}

// ValidateTaxId checks whether the given tax ID is valid according to the VIES service.
// The taxId is expected to start with a 2-letter country code (e.g. "DE123456789");
// the country code is sent separately from the number, as VIES requires.
//
// Returns VIESValid / VIESInvalid for definitive verdicts, or VIESInconclusive when the
// VIES service is unavailable or rate-limited (transient userError codes). Inconclusive
// results are never cached and must not be reported as invalid. Errors are logged but
// never block the caller.
func (s *Service) ValidateTaxId(taxId string) entity.VIESResult {
	if len(taxId) < 3 {
		s.log.Warn("tax ID too short for VIES validation", slog.String("tax_id", taxId))
		return entity.VIESInvalid
	}

	countryCode := taxId[:2]
	vatNumber := taxId[2:]

	// Check MongoDB cache first. Only definitive verdicts are ever stored, so a fresh
	// cache hit is always conclusive.
	if s.db != nil {
		cached, err := s.db.GetVIESValidation(countryCode, vatNumber)
		if err != nil {
			s.log.Warn("get VIES cache", slog.String("country", countryCode), sl.Err(err))
		} else if cached != nil {
			age := time.Since(cached.ValidatedAt)
			if age < s.cacheAge {
				s.log.Debug("VIES cache hit",
					slog.String("country", countryCode),
					slog.String("vat_number", vatNumber),
					slog.Bool("valid", cached.Valid),
					slog.String("age", age.Truncate(time.Minute).String()))
				return boolToResult(cached.Valid)
			}
			// Cache is stale — try API, fall back to stale result below.
		}
	}

	// Call VIES API
	resp, err := s.checkVATNumber(countryCode, vatNumber)
	if err != nil {
		s.log.Warn("VIES API call failed",
			slog.String("country", countryCode),
			slog.String("vat_number", vatNumber),
			sl.Err(err))
		return s.staleOr(countryCode, vatNumber, "API failure")
	}

	result := resp.result()

	// A transient/service error (e.g. MS_MAX_CONCURRENT_REQ) is not a verdict: the number
	// may well be valid. Do not cache it and do not report it as invalid — fall back to a
	// prior definitive result if we have one, otherwise report inconclusive.
	if result == entity.VIESInconclusive {
		s.log.Warn("VIES validation inconclusive",
			slog.String("country", countryCode),
			slog.String("vat_number", vatNumber),
			slog.String("user_error", resp.UserError))
		return s.staleOr(countryCode, vatNumber, "inconclusive result")
	}

	// Definitive verdict — persist it.
	validation := &entity.VIESValidation{
		CountryCode:       countryCode,
		VATNumber:         vatNumber,
		RequestDate:       resp.RequestDate,
		Valid:             result == entity.VIESValid,
		Name:              resp.Name,
		Address:           resp.Address,
		RequestIdentifier: resp.RequestIdentifier,
		ValidatedAt:       time.Now(),
	}
	if s.db != nil {
		if err := s.db.SaveVIESValidation(validation); err != nil {
			s.log.Warn("save VIES validation", slog.String("country", countryCode), sl.Err(err))
		}
	}

	s.log.With(
		slog.String("country", countryCode),
		slog.String("vat_number", vatNumber),
		slog.String("name", validation.Name),
		slog.Bool("valid", validation.Valid),
		slog.String("validated_at", validation.ValidatedAt.Format(time.RFC3339)),
	).Debug("VIES validation completed")

	return result
}

// staleOr returns a cached definitive verdict when the live check could not produce one,
// or VIESInconclusive if no cached result exists. reason is used only for logging.
func (s *Service) staleOr(countryCode, vatNumber, reason string) entity.VIESResult {
	if s.db != nil {
		cached, err := s.db.GetVIESValidation(countryCode, vatNumber)
		if err == nil && cached != nil {
			s.log.Debug("using stale VIES cache after "+reason,
				slog.String("country", countryCode),
				slog.Bool("valid", cached.Valid))
			return boolToResult(cached.Valid)
		}
	}
	return entity.VIESInconclusive
}

// checkVATNumber sends a GET request to the VIES REST API.
func (s *Service) checkVATNumber(countryCode, vatNumber string) (*viesResponse, error) {
	url := fmt.Sprintf(baseURL, countryCode, vatNumber)

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := s.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("VIES API status %d: %s", resp.StatusCode, string(respBody))
	}

	var vr viesResponse
	if err := json.Unmarshal(respBody, &vr); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &vr, nil
}
