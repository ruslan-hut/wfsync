package wfirma

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"time"
	"wfsync/lib/sl"
)

// VAT codes for cross-border transactions (passed in invoicecontent "vat" field).
const (
	vatWDT  = "WDT"  // 0% intra-community goods delivery (EU buyer with VAT number)
	vatEXP  = "EXP"  // 0% export of goods (non-EU buyer)
	vatNP   = "NP"   // not subject to Polish VAT (non-EU services)
	vatNPUE = "NPUE" // not subject to Polish VAT, EU reverse charge (EU services)
	vatZW   = "ZW"   // exempt from VAT
)

// polishVatCodes contains VAT code strings accepted by the wFirma "vat" field.
// Any rate not in this set (e.g. "25" for Denmark, "21" for Netherlands) must be sent
// via the "vat_code" object reference with the numeric ID from vat_codes/findAll.
var polishVatCodes = map[string]bool{
	"23": true, "22": true, "8": true, "7": true, "5": true, "3": true, "0": true,
	vatWDT: true, vatEXP: true, vatNP: true, vatNPUE: true, vatZW: true,
}

// b2bCustomerGroups contains OpenCart customer group IDs that represent B2B customers.
// B2B customers with a TaxID in the EU get WDT (0%), without TaxID get 23% Polish rate.
// B2C customers always get the destination-country rate regardless of TaxID.
var b2bCustomerGroups = map[int]bool{
	6: true, 7: true, 16: true, 18: true, 19: true,
}

// euCountries contains EU member state codes (ISO 3166-1 alpha-2), excluding Poland.
// Used as a fallback when the dynamic VATProvider is not available or unverified.
var euCountries = map[string]bool{
	"AT": true, "BE": true, "BG": true, "HR": true, "CY": true,
	"CZ": true, "DK": true, "EE": true, "FI": true, "FR": true,
	"DE": true, "GR": true, "HU": true, "IE": true, "IT": true,
	"LV": true, "LT": true, "LU": true, "MT": true, "NL": true,
	"PT": true, "RO": true, "SK": true, "SI": true, "ES": true,
	"SE": true,
}

// vatCodesTTL is how long fetched VAT codes stay valid before a refresh is attempted.
const vatCodesTTL = 24 * time.Hour

// resolveGoodsVatCode determines the correct VAT code for invoice line items.
// The company is registered under the EU OSS (One-Stop Shop) scheme, so the site
// calculates the destination-country VAT rate and we pass it through to wfirma.
//
// B2B rules (OpenCart customer groups 6, 7, 16, 18, 19):
//   - PL or unknown country → numeric rate from the order (e.g. "23")
//   - EU country + VAT number → "WDT" (intra-community delivery, 0%)
//   - EU country without VAT number → "23" (Polish rate, not destination rate)
//   - Non-EU country → "EXP" (export, 0%)
//
// B2C rules (all other customer groups):
//   - PL or unknown country → numeric rate from the order (e.g. "23")
//   - EU country → destination-country rate (e.g. "21" for NL, "19" for DE), TaxID irrelevant
//   - Non-EU country → "EXP" (export, 0%)
func resolveGoodsVatCode(taxRate int, countryCode string, hasTaxId bool, b2b bool, vp VATProvider) string {
	if countryCode == "" || countryCode == "PL" {
		return strconv.Itoa(taxRate)
	}

	isEU := false
	if vp != nil {
		isEU = vp.IsEUCountry(countryCode)
	} else {
		isEU = euCountries[countryCode]
	}

	if !isEU {
		return vatEXP
	}
	// EU country — branch on B2B vs B2C
	if b2b {
		if hasTaxId {
			return vatWDT
		}
		return "23"
	}
	// B2C: destination-country rate, TaxID irrelevant
	return strconv.Itoa(taxRate)
}

// formatCustomerGroup returns a human-readable label like "3 (B2C)" or "6 (B2B)".
func formatCustomerGroup(group int) string {
	if b2bCustomerGroups[group] {
		return fmt.Sprintf("%d (B2B)", group)
	}
	return fmt.Sprintf("%d (B2C)", group)
}

// fetchVatCodes retrieves all VAT codes from the wFirma API (vat_codes/find)
// and returns a map of code string → entity ID.
// This is needed because non-Polish VAT rates (e.g. "25" for DK) require the
// vat_code object reference — the plain "vat" string field only accepts Polish codes.
func (c *Client) fetchVatCodes(ctx context.Context) (map[string]int64, error) {
	const pageSize = 100
	result := make(map[string]int64)

	for page := 0; ; page++ {
		payload := map[string]interface{}{
			"api": map[string]interface{}{
				"vat_codes": map[string]interface{}{
					"parameters": map[string]interface{}{
						"limit": pageSize,
						"page":  page,
					},
				},
			},
		}

		res, err := c.request(ctx, "vat_codes", "find", payload)
		if err != nil {
			return nil, fmt.Errorf("fetch vat codes page %d: %w", page, err)
		}

		var resp VatCodesResponse
		if err = json.Unmarshal(res, &resp); err != nil {
			return nil, fmt.Errorf("parse vat codes response: %w", err)
		}
		if resp.Status.Code == "ERROR" {
			return nil, fmt.Errorf("vat_codes API error")
		}

		for _, wrapper := range resp.VatCodes {
			vc := wrapper.VatCode
			if vc.ID == "" || vc.Code == "" {
				continue
			}
			id, err := strconv.ParseInt(vc.ID, 10, 64)
			if err != nil {
				continue
			}
			result[vc.Code] = id
		}

		fetched := (page + 1) * pageSize
		if fetched >= resp.Parameters.Total || len(resp.VatCodes) == 0 {
			break
		}
	}

	c.log.Info("vat codes loaded", slog.Int("count", len(result)), slog.Any("codes", result))
	return result, nil
}

// getVatCodeId looks up the wFirma vat_code entity ID for a given code string.
// Thread-safe. Lazily fetches on first call and refreshes after vatCodesTTL.
// Keeps stale cache on refresh failure. Returns 0 if the code is not found.
func (c *Client) getVatCodeId(ctx context.Context, code string) int64 {
	c.vatCodesMu.RLock()
	id, ok := c.vatCodes[code]
	stale := c.vatCodes == nil || time.Since(c.vatCodesAt) > vatCodesTTL
	c.vatCodesMu.RUnlock()

	if !stale && ok {
		return id
	}
	if !stale {
		return 0
	}

	// Cache is empty or expired — refresh under write lock.
	c.vatCodesMu.Lock()
	defer c.vatCodesMu.Unlock()

	// Double-check after acquiring write lock (another goroutine may have refreshed).
	if c.vatCodes != nil && time.Since(c.vatCodesAt) <= vatCodesTTL {
		return c.vatCodes[code]
	}

	codes, err := c.fetchVatCodes(ctx)
	if err != nil {
		c.log.Warn("fetch vat codes failed, using stale cache", sl.Err(err))
		// Keep stale cache — return whatever we have.
		return c.vatCodes[code]
	}
	c.vatCodes = codes
	c.vatCodesAt = time.Now()
	return c.vatCodes[code]
}

// setContentVat sets the VAT on a Content line item.
// For standard Polish codes (23, 8, 5, etc.) it uses the "vat" string field.
// For non-Polish rates (EU OSS) it looks up the vat_code entity ID and uses the "vat_code" reference.
// Falls back to the "vat" string field if the vat_code lookup fails.
func (c *Client) setContentVat(ctx context.Context, content *Content, vatCode string) {
	if polishVatCodes[vatCode] {
		content.Vat = vatCode
		return
	}
	// Non-Polish rate — resolve via vat_code reference.
	if id := c.getVatCodeId(ctx, vatCode); id > 0 {
		content.VatCode = &VatCodeRef{ID: id}
		return
	}
	// Fallback: send as vat string (may be ignored by the API).
	c.log.Warn("vat_code not found, falling back to vat string",
		slog.String("code", vatCode))
	content.Vat = vatCode
}
