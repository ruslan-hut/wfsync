// Package vatrates fetches and caches standard EU VAT rates from the vatlookup.eu API.
// It provides dynamic EU country membership and rate lookups, replacing hardcoded maps.
// Rates are refreshed periodically in the background; stale cache is kept on fetch failure.
// When a Database is configured, rates are persisted to MongoDB and loaded on startup
// to avoid unnecessary API calls after restarts.
package vatrates

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
	"wfsync/entity"
	"wfsync/internal/config"
	"wfsync/lib/sl"
)

const baseURL = "https://api.vatlookup.eu"

// retryInterval is used instead of the normal refresh interval when the service is unverified.
const retryInterval = 30 * time.Minute

// Database defines the persistence methods the VAT rates service needs.
type Database interface {
	SaveVATRate(rate *entity.VATRate) error
	GetAllVATRates() ([]*entity.VATRate, error)
}

// countryEntry represents a single item from the /countrylist/ endpoint.
type countryEntry struct {
	Code      string `json:"code"`
	VATPrefix string `json:"vat_prefix"`
	Name      string `json:"name"`
}

// ratesResponse represents the response from the /rates/{code}/ endpoint.
type ratesResponse struct {
	Rates []rateGroup `json:"rates"`
}

type rateGroup struct {
	Name  string    `json:"name"`
	Rates []float64 `json:"rates"`
}

// Service fetches EU VAT rates from vatlookup.eu and caches them in memory.
// The service tracks a "verified" flag that indicates whether the in-memory rates
// have been confirmed against the database. Consumers should check Verified()
// and fall back to their own logic when false.
type Service struct {
	hc              *http.Client
	refreshInterval time.Duration
	log             *slog.Logger
	db              Database
	trustDB         bool

	mu       sync.RWMutex
	rates    map[string]float64 // country code (ISO alpha-2) → standard VAT rate
	verified bool               // true when rates have been confirmed consistent with DB

	done    chan struct{}
	stopped chan struct{}
}

// New creates a VAT rates service. Call Start() to begin background refresh.
// When conf.VATRates.TrustDB is true, the service starts as verified after
// loading data from the database, allowing consumers to use it immediately.
func New(conf *config.Config, log *slog.Logger) *Service {
	hours := conf.VATRates.RefreshHours
	if hours <= 0 {
		hours = 24
	}
	return &Service{
		hc:              &http.Client{Timeout: 20 * time.Second},
		refreshInterval: time.Duration(hours) * time.Hour,
		log:             log.With(sl.Module("vatrates")),
		rates:           make(map[string]float64),
		trustDB:         conf.VATRates.TrustDB,
	}
}

// SetDatabase configures optional MongoDB persistence.
// When set, rates are loaded from DB on startup and persisted on each API refresh.
func (s *Service) SetDatabase(db Database) {
	s.db = db
}

// IsEUCountry reports whether the given ISO alpha-2 country code is an EU member (excluding PL).
func (s *Service) IsEUCountry(code string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.rates[code]
	return ok
}

// GetStandardRate returns the standard VAT rate for the given country, or 0 if unknown.
func (s *Service) GetStandardRate(code string) float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rates[code]
}

// Verified reports whether the service data has been confirmed consistent with the database.
// When false, consumers should fall back to their own hardcoded data.
func (s *Service) Verified() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.verified
}

// nextInterval returns retryInterval when unverified, or the normal refresh interval otherwise.
func (s *Service) nextInterval() time.Duration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.verified {
		return retryInterval
	}
	return s.refreshInterval
}

// Start launches the background refresh goroutine.
// It tries to load cached rates from DB first, falling back to an API call
// when the DB is empty or the data is older than the refresh interval.
func (s *Service) Start() {
	s.done = make(chan struct{})
	s.stopped = make(chan struct{})
	go func() {
		defer close(s.stopped)

		// Try loading from DB first
		newestUpdatedAt := s.loadFromDB()

		// Fetch from API if cache is empty or stale
		s.mu.RLock()
		cacheLen := len(s.rates)
		s.mu.RUnlock()

		if cacheLen == 0 || time.Since(newestUpdatedAt) > s.refreshInterval {
			if cacheLen == 0 {
				s.log.Info("vat rates: db empty or stale, fetching from API")
			} else {
				s.log.Info("vat rates: db data stale, refreshing from API",
					slog.String("age", time.Since(newestUpdatedAt).Truncate(time.Minute).String()))
			}
			s.refreshFromAPI()
		}

		ticker := time.NewTicker(s.nextInterval())
		defer ticker.Stop()
		for {
			select {
			case <-s.done:
				s.log.Debug("vat rates refresh stopped")
				return
			case <-ticker.C:
				s.refreshFromAPI()
				ticker.Reset(s.nextInterval())
			}
		}
	}()
}

// Stop signals the background goroutine to exit and waits for it to finish.
func (s *Service) Stop() {
	if s.done != nil {
		s.log.Debug("stopping vat rates refresh")
		close(s.done)
		<-s.stopped
	}
}

// loadFromDB populates the in-memory cache from MongoDB.
// Returns the newest UpdatedAt timestamp across all loaded rates (zero if DB is nil or empty).
func (s *Service) loadFromDB() time.Time {
	var newest time.Time
	if s.db == nil {
		return newest
	}

	rates, err := s.db.GetAllVATRates()
	if err != nil {
		s.log.Error("load vat rates from db", sl.Err(err))
		return newest
	}
	if len(rates) == 0 {
		return newest
	}

	loaded := make(map[string]float64, len(rates))
	for _, r := range rates {
		loaded[r.CountryCode] = r.StandardRate
		if r.UpdatedAt.After(newest) {
			newest = r.UpdatedAt
		}
	}

	s.mu.Lock()
	s.rates = loaded
	s.verified = s.trustDB
	s.mu.Unlock()

	if s.trustDB {
		s.log.Info("vat rates loaded from db (trusted)", slog.Int("countries", len(loaded)))
	} else {
		s.log.Info("vat rates loaded from db (unverified, waiting for API refresh)", slog.Int("countries", len(loaded)))
	}
	return newest
}

// refreshFromAPI fetches rates from the external API, updates the in-memory cache,
// and persists each rate to the database (if configured).
func (s *Service) refreshFromAPI() {
	countries, err := s.fetchCountryList()
	if err != nil {
		s.log.Error("fetch country list", sl.Err(err))
		return
	}

	now := time.Now()
	newRates := make(map[string]float64, len(countries))
	for _, c := range countries {
		code := c.Code

		// Skip UK and PL
		if code == "UK" || code == "XI" || code == "PL" {
			continue
		}

		rate, err := s.fetchStandardRate(code)
		if err != nil {
			s.log.Warn("fetch rate", slog.String("country", code), sl.Err(err))
			continue
		}

		countryName := c.Name

		// Map EL (Greece in EU VAT system) to GR (ISO 3166)
		if code == "EL" {
			code = "GR"
		}

		if rate > 0 {
			newRates[code] = rate

			// Persist to DB
			if s.db != nil {
				if err := s.db.SaveVATRate(&entity.VATRate{
					CountryCode:  code,
					StandardRate: rate,
					CountryName:  countryName,
					UpdatedAt:    now,
				}); err != nil {
					s.log.Warn("save vat rate to db", slog.String("country", code), sl.Err(err))
				}
			}
		}
	}

	// Only swap if we got results; keep stale cache on total failure
	if len(newRates) == 0 {
		s.log.Warn("vat rates refresh returned zero countries, keeping existing cache")
		return
	}

	// Verify DB persistence: the saved count must match what we fetched from the API.
	// On mismatch, mark the service as unverified so consumers fall back to their own data.
	dbVerified := true
	if s.db != nil {
		dbRates, err := s.db.GetAllVATRates()
		if err != nil {
			s.log.Error("verify vat rates in db", sl.Err(err))
			dbVerified = false
		} else if len(dbRates) != len(newRates) {
			s.log.Error("vat rates db count mismatch",
				slog.Int("api_count", len(newRates)),
				slog.Int("db_count", len(dbRates)),
			)
			dbVerified = false
		}
	}

	s.mu.Lock()
	s.rates = newRates
	s.verified = dbVerified
	s.mu.Unlock()

	if dbVerified {
		s.log.Info("vat rates refreshed and verified", slog.Int("countries", len(newRates)))
	} else {
		s.log.Warn("vat rates refreshed but NOT verified, consumers will use fallback",
			slog.Int("countries", len(newRates)))
	}
}

// fetchCountryList calls GET /countrylist/ and returns the list of EU countries.
func (s *Service) fetchCountryList() ([]countryEntry, error) {
	resp, err := s.hc.Get(baseURL + "/countrylist/")
	if err != nil {
		return nil, fmt.Errorf("GET countrylist: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("countrylist status %d", resp.StatusCode)
	}

	var countries []countryEntry
	if err = json.NewDecoder(resp.Body).Decode(&countries); err != nil {
		return nil, fmt.Errorf("decode countrylist: %w", err)
	}
	return countries, nil
}

// fetchStandardRate calls GET /rates/{code}/ and extracts the first "Standard" rate.
func (s *Service) fetchStandardRate(code string) (float64, error) {
	resp, err := s.hc.Get(fmt.Sprintf("%s/rates/%s/", baseURL, code))
	if err != nil {
		return 0, fmt.Errorf("GET rates/%s: %w", code, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("rates/%s status %d", code, resp.StatusCode)
	}

	var rr ratesResponse
	if err = json.NewDecoder(resp.Body).Decode(&rr); err != nil {
		return 0, fmt.Errorf("decode rates/%s: %w", code, err)
	}

	for _, g := range rr.Rates {
		if g.Name == "Standard" && len(g.Rates) > 0 {
			return g.Rates[0], nil
		}
	}
	return 0, nil
}
