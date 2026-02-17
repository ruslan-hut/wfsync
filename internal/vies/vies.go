// Package vies validates EU VAT numbers against the official VIES REST API.
// Results are cached in MongoDB to reduce API calls; stale cache is used as fallback
// when the API is unavailable. Validation is on-demand (no background goroutine).
package vies

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
	"wfsync/entity"
	"wfsync/internal/config"
	"wfsync/lib/sl"
)

const (
	prodURL = "https://ec.europa.eu/taxation_customs/vies/rest-api/check-vat-number"
	testURL = "https://ec.europa.eu/taxation_customs/vies/rest-api/check-vat-test-service"
)

// Database defines the persistence methods the VIES service needs.
type Database interface {
	SaveVIESValidation(v *entity.VIESValidation) error
	GetVIESValidation(countryCode, vatNumber string) (*entity.VIESValidation, error)
}

// viesRequest is the JSON body sent to the VIES REST API.
type viesRequest struct {
	CountryCode string `json:"countryCode"`
	VATNumber   string `json:"vatNumber"`
}

// viesResponse is the JSON body returned by the VIES REST API.
type viesResponse struct {
	IsValid           bool   `json:"isValid"`
	RequestDate       string `json:"requestDate"`
	UserError         string `json:"userError"`
	Name              string `json:"name"`
	Address           string `json:"address"`
	RequestIdentifier string `json:"requestIdentifier"`
	VATNumber         string `json:"vatNumber"`
}

// Service validates EU VAT numbers via the VIES API with MongoDB caching.
type Service struct {
	hc       *http.Client
	log      *slog.Logger
	db       Database
	cacheAge time.Duration
	baseURL  string
}

// New creates a VIES validation service.
func New(conf *config.Config, log *slog.Logger) *Service {
	hours := conf.VIES.CacheHours
	if hours <= 0 {
		hours = 720
	}
	baseURL := prodURL
	if conf.VIES.TestMode {
		baseURL = testURL
	}
	return &Service{
		hc:       &http.Client{Timeout: 20 * time.Second},
		log:      log.With(sl.Module("vies")),
		cacheAge: time.Duration(hours) * time.Hour,
		baseURL:  baseURL,
	}
}

// SetDatabase configures optional MongoDB persistence for caching validation results.
func (s *Service) SetDatabase(db Database) {
	s.db = db
}

// ValidateTaxId checks whether the given tax ID is valid according to the VIES service.
// The taxId is expected to start with a 2-letter country code (e.g. "DE123456789").
// Returns true if valid, false otherwise. Errors are logged but do not block the caller.
func (s *Service) ValidateTaxId(taxId string) bool {
	if len(taxId) < 3 {
		s.log.Warn("tax ID too short for VIES validation", slog.String("tax_id", taxId))
		return false
	}

	countryCode := taxId[:2]
	vatNumber := taxId[2:]

	// Check MongoDB cache first
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
				return cached.Valid
			}
			// Cache is stale — try API, fall back to stale result below
		}
	}

	// Call VIES API
	resp, err := s.checkVATNumber(countryCode, vatNumber)
	if err != nil {
		s.log.Warn("VIES API call failed",
			slog.String("country", countryCode),
			slog.String("vat_number", vatNumber),
			sl.Err(err))

		// Fall back to stale cache if available
		if s.db != nil {
			cached, dbErr := s.db.GetVIESValidation(countryCode, vatNumber)
			if dbErr == nil && cached != nil {
				s.log.Debug("using stale VIES cache after API failure",
					slog.String("country", countryCode),
					slog.Bool("valid", cached.Valid))
				return cached.Valid
			}
		}
		return false
	}

	// Save result to MongoDB
	validation := &entity.VIESValidation{
		CountryCode:       countryCode,
		VATNumber:         vatNumber,
		RequestDate:       resp.RequestDate,
		Valid:             resp.IsValid,
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

	return resp.IsValid
}

// checkVATNumber sends a POST request to the VIES REST API.
func (s *Service) checkVATNumber(countryCode, vatNumber string) (*viesResponse, error) {
	body, err := json.Marshal(viesRequest{
		CountryCode: countryCode,
		VATNumber:   vatNumber,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, s.baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := s.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", s.baseURL, err)
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
