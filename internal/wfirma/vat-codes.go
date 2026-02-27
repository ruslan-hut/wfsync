package wfirma

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

// fetchVatCodes retrieves all available VAT codes from the wFirma API (vat_codes/find)
// and caches them as a name→ID map (Polish codes) and countryID→ID map (foreign/OSS codes).
// Called lazily on first invoice creation.
func (c *Client) fetchVatCodes(ctx context.Context) error {
	polishCodes := make(map[string]string)
	// ossCodesByCountry maps declaration_country_id → vat_code_id for the standard rate.
	ossCodesByCountry := make(map[string]string)

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
				// Foreign (OSS) code — store the first encountered per country
				// (wFirma returns the standard rate first).
				if _, ok := ossCodesByCountry[dcID]; !ok {
					ossCodesByCountry[dcID] = vc.ID
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
	return nil
}

// lookupDeclarationCountryID finds the wFirma declaration_country ID for a given
// ISO 3166-1 alpha-2 country code using a filtered search.
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

	vcID, ok := c.ossVatCodes[dcID]
	if !ok {
		c.log.Warn("OSS vat code not found for declaration country",
			slog.String("country", countryCode),
			slog.String("declaration_country_id", dcID))
		return ""
	}

	c.log.Debug("resolved OSS vat code",
		slog.String("country", countryCode),
		slog.String("declaration_country_id", dcID),
		slog.String("vat_code_id", vcID),
		slog.String("expected_rate", expectedRate))
	return vcID
}

