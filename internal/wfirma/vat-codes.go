package wfirma

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
)

// normalizeRate parses a VAT rate string ("27", "27.00", "5.0") and returns
// a canonical form ("27", "5") for use as a map key. Returns "" on parse error.
func normalizeRate(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return ""
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// fetchVatCodes retrieves all available VAT codes from the wFirma API (vat_codes/find)
// and caches them as a name→ID map (Polish codes) and countryID→ID map (foreign/OSS codes).
// Called lazily on first invoice creation. Caller must hold c.cacheMu.
func (c *Client) fetchVatCodes(ctx context.Context) error {
	polishCodes := make(map[string]string)
	// ossCodesByCountry maps declaration_country_id → normalized rate → vat_code_id.
	// Countries can have multiple rates (standard + reduced), so we must key by rate
	// rather than assuming the first returned code is the standard rate.
	ossCodesByCountry := make(map[string]map[string]string)

	const pageLimit = 100
	// wFirma uses 1-based pages.
	for page := 1; ; page++ {
		payload := map[string]interface{}{
			"api": map[string]interface{}{
				"vat_codes": map[string]interface{}{
					"parameters": map[string]interface{}{
						"limit": pageLimit,
						"page":  page,
					},
				},
			},
		}

		res, err := c.request(ctx, "vat_codes", "find", payload)
		if err != nil {
			return fmt.Errorf("vat_codes/find page %d: %w", page, err)
		}

		var resp VatCodesResponse
		if err = json.Unmarshal(res, &resp); err != nil {
			return fmt.Errorf("parse vat_codes response page %d: %w", page, err)
		}
		if resp.Status.Code == "ERROR" {
			return fmt.Errorf("vat_codes/find returned error: %s", resp.Status.Message)
		}

		for _, wrapper := range resp.VatCodes {
			vc := wrapper.VatCode
			if vc.ID == "" {
				continue
			}
			dcID := ""
			if vc.DeclarationCountry != nil {
				dcID = vc.DeclarationCountry.ID
			}

			if vc.Code != "" {
				// Polish codes have a non-empty short code (e.g. "23", "WDT").
				polishCodes[vc.Code] = vc.ID
			} else if dcID != "" && dcID != "0" {
				// Foreign (OSS) code — index by normalized rate so the caller can
				// pick the matching rate (e.g. HU has both 5% and 27%).
				rateKey := normalizeRate(vc.Rate)
				if rateKey == "" {
					continue
				}
				if ossCodesByCountry[dcID] == nil {
					ossCodesByCountry[dcID] = make(map[string]string)
				}
				if _, ok := ossCodesByCountry[dcID][rateKey]; !ok {
					ossCodesByCountry[dcID][rateKey] = vc.ID
				}
			}
		}

		// If we got fewer results than the page limit, this was the last page.
		if len(resp.VatCodes) < pageLimit {
			break
		}
	}

	c.vatCodes = polishCodes
	c.ossVatCodes = ossCodesByCountry
	c.log.Debug("vat codes cached",
		slog.Int("polish", len(polishCodes)),
		slog.Int("oss", len(ossCodesByCountry)))

	// Diagnostic dump: per-declaration-country OSS rate sets, so missing
	// foreign rates (e.g. PT 23%) are visible without debug-level logging.
	dcCodeByID := make(map[string]string, len(c.declCountries))
	for code, id := range c.declCountries {
		dcCodeByID[id] = code
	}
	for dcID, rates := range ossCodesByCountry {
		c.log.Info("oss vat codes for declaration country",
			slog.String("declaration_country_id", dcID),
			slog.String("country", dcCodeByID[dcID]),
			slog.Any("rates", rates))
	}
	return nil
}

// lookupDeclarationCountryID finds the wFirma declaration_country ID for a given
// ISO 3166-1 alpha-2 country code using a filtered search. Caller must hold c.cacheMu.
func (c *Client) lookupDeclarationCountryID(ctx context.Context, countryCode string) (string, error) {
	// Check cache first.
	if c.declCountries != nil {
		if id, ok := c.declCountries[strings.ToUpper(countryCode)]; ok {
			return id, nil
		}
	}

	// Search by exact ISO code using wFirma conditions filter.
	payload := map[string]interface{}{
		"api": map[string]interface{}{
			"declaration_countries": map[string]interface{}{
				"parameters": map[string]interface{}{
					"limit": 10,
					"conditions": map[string]interface{}{
						"and": []map[string]interface{}{
							{
								"condition": map[string]interface{}{
									"field":    "code",
									"operator": "eq",
									"value":    strings.ToUpper(countryCode),
								},
							},
						},
					},
				},
			},
		},
	}

	res, err := c.request(ctx, "declaration_countries", "find", payload)
	if err != nil {
		return "", fmt.Errorf("declaration_countries/find for %s: %w", countryCode, err)
	}

	var resp DeclarationCountryResponse
	if err = json.Unmarshal(res, &resp); err != nil {
		return "", fmt.Errorf("parse declaration_countries response: %w", err)
	}
	if resp.Status.Code == "ERROR" {
		return "", fmt.Errorf("declaration_countries/find error: %s", resp.Status.Message)
	}

	for _, wrapper := range resp.Countries {
		dc := wrapper.Country
		if dc.ID != "" && strings.EqualFold(dc.Code, countryCode) {
			// Cache the result for future lookups.
			if c.declCountries == nil {
				c.declCountries = make(map[string]string)
			}
			c.declCountries[strings.ToUpper(countryCode)] = dc.ID
			c.log.Debug("declaration country resolved",
				slog.String("country", countryCode),
				slog.String("id", dc.ID))
			return dc.ID, nil
		}
	}

	return "", fmt.Errorf("declaration country not found for code %s", countryCode)
}

// resolveVatCodeID looks up the wFirma vat_code ID for a given Polish VAT code string
// (e.g. "23", "WDT", "EXP"). Fetches and caches vat codes on first call.
// Returns empty string if the code is not found or fetching fails.
func (c *Client) resolveVatCodeID(ctx context.Context, code string) string {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	if c.vatCodes == nil {
		if err := c.fetchVatCodes(ctx); err != nil {
			c.log.Warn("fetch vat codes, falling back to vat field", slog.String("error", err.Error()))
			return ""
		}
	}

	if id, ok := c.vatCodes[code]; ok {
		return id
	}

	c.log.Warn("vat code ID not found, will retry on next invoice",
		slog.String("code", code),
		slog.Any("available", c.vatCodes))
	// Reset cache so the next invoice creation retries the fetch.
	c.vatCodes = nil
	c.ossVatCodes = nil
	return ""
}

// resolveOSSVatCodeIDWithRate resolves the foreign vat_code ID for an EU country.
// Chains: ISO country code → declaration_country_id → vat_code_id.
// The expectedRate is logged for debugging but the mapping is by country, not rate.
func (c *Client) resolveOSSVatCodeIDWithRate(ctx context.Context, countryCode string, expectedRate string) string {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	// Look up the declaration country ID for this ISO code.
	dcID, err := c.lookupDeclarationCountryID(ctx, countryCode)
	if err != nil {
		c.log.Warn("declaration country lookup failed",
			slog.String("country", countryCode),
			slog.String("error", err.Error()))
		return ""
	}

	// Ensure vat codes (including OSS) are loaded.
	if c.ossVatCodes == nil {
		if err := c.fetchVatCodes(ctx); err != nil {
			c.log.Warn("fetch vat codes for OSS", slog.String("error", err.Error()))
			return ""
		}
	}

	rates, ok := c.ossVatCodes[dcID]
	if !ok || len(rates) == 0 {
		c.log.Warn("OSS vat code not found for declaration country",
			slog.String("country", countryCode),
			slog.String("declaration_country_id", dcID))
		return ""
	}

	rateKey := normalizeRate(expectedRate)
	if vcID, ok := rates[rateKey]; ok {
		c.log.Debug("resolved OSS vat code",
			slog.String("country", countryCode),
			slog.String("declaration_country_id", dcID),
			slog.String("vat_code_id", vcID),
			slog.String("rate", rateKey))
		return vcID
	}

	// No exact rate match — fall back to the lowest available rate that is still
	// >= the requested rate. Never return a lower rate: that would silently
	// under-charge VAT. If nothing qualifies, return "" so the caller falls back
	// to the plain "vat" field (wFirma resets it to the Polish rate).
	requestedVal, err := strconv.ParseFloat(rateKey, 64)
	if err != nil {
		c.log.Warn("OSS vat code: requested rate unparseable, no fallback",
			slog.String("country", countryCode),
			slog.String("declaration_country_id", dcID),
			slog.String("requested_rate", expectedRate),
			slog.Any("available_rates", rates))
		return ""
	}
	var fallbackRate, fallbackID string
	var fallbackVal float64
	for r, id := range rates {
		v, err := strconv.ParseFloat(r, 64)
		if err != nil || v < requestedVal {
			continue
		}
		if fallbackID == "" || v < fallbackVal {
			fallbackVal, fallbackRate, fallbackID = v, r, id
		}
	}
	if fallbackID == "" {
		c.log.Warn("OSS vat code: no rate >= requested available, no fallback",
			slog.String("country", countryCode),
			slog.String("declaration_country_id", dcID),
			slog.String("requested_rate", rateKey),
			slog.Any("available_rates", rates))
		return ""
	}
	c.log.Warn("OSS vat code: requested rate not found, using lowest rate >= requested",
		slog.String("country", countryCode),
		slog.String("declaration_country_id", dcID),
		slog.String("requested_rate", rateKey),
		slog.String("fallback_rate", fallbackRate),
		slog.Any("available_rates", rates))
	return fallbackID
}
