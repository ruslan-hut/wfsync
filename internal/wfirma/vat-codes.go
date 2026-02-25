package wfirma

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
)

// fetchVatCodes retrieves all available VAT codes from the wFirma API (vat_codes/find)
// and caches them as a name→ID map. Called lazily on first invoice creation.
func (c *Client) fetchVatCodes(ctx context.Context) error {
	payload := map[string]interface{}{
		"api": map[string]interface{}{
			"vat_codes": map[string]interface{}{
				"parameters": map[string]interface{}{
					"limit": 100,
				},
			},
		},
	}

	res, err := c.request(ctx, "vat_codes", "find", payload)
	if err != nil {
		return fmt.Errorf("vat_codes/find: %w", err)
	}

	c.log.Debug("vat_codes/find raw response", slog.String("body", string(res)))

	var resp VatCodesResponse
	if err = json.Unmarshal(res, &resp); err != nil {
		return fmt.Errorf("parse vat_codes response: %w", err)
	}
	if resp.Status.Code == "ERROR" {
		return fmt.Errorf("vat_codes/find returned error")
	}

	codes := make(map[string]string, len(resp.VatCodes))
	for _, wrapper := range resp.VatCodes {
		vc := wrapper.VatCode
		if vc.ID != "" && vc.Name != "" {
			codes[vc.Name] = vc.ID
		}
	}

	c.vatCodes = codes
	c.log.Debug("vat codes cached",
		slog.Int("count", len(codes)),
		slog.Any("codes", codes))
	return nil
}

// resolveVatCodeID looks up the wFirma vat_code ID for a given VAT code string
// (e.g. "23", "WDT", "EXP"). Fetches and caches vat codes on first call.
// Returns empty string if the code is not found or fetching fails.
// On fetch failure the cache is reset to nil so the next invoice retries.
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
	return ""
}
