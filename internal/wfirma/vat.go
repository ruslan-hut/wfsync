package wfirma

import (
	"fmt"
	"strconv"
)

// VAT codes for cross-border transactions (passed in invoicecontent "vat" field).
const (
	vatWDT  = "WDT"  // 0% intra-community goods delivery (EU buyer with VAT number)
	vatEXP  = "EXP"  // 0% export of goods (non-EU buyer)
	vatNP   = "NP"   // not subject to Polish VAT (non-EU services)
	vatNPUE = "NPUE" // not subject to Polish VAT, EU reverse charge (EU services)
	vatZW   = "ZW"   // exempt from VAT
)

// b2bCustomerGroups contains customer group IDs that represent B2B customers.
// B2B customers with a TaxID in the EU get WDT (0%), without TaxID get 23% Polish rate.
// B2C customers always get the destination-country rate regardless of TaxID.
//
// -1 is a synthetic B2B flag for direct API callers (POST /v1/wf/invoice, /v1/wf/proforma)
// who don't have an OpenCart customer group. IDs 6, 7, 16, 18, 19 are OpenCart B2B groups.
var b2bCustomerGroups = map[int]bool{
	-1: true, 6: true, 7: true, 16: true, 18: true, 19: true,
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

// defaultEURates maps EU country codes to their standard VAT rates (as of 2025).
// Used as a last-resort fallback when the caller doesn't provide tax_value and the
// dynamic VATProvider is unavailable. Rates may drift — the VATProvider is preferred.
var defaultEURates = map[string]int{
	"AT": 20, "BE": 21, "BG": 20, "HR": 25, "CY": 19,
	"CZ": 21, "DK": 25, "EE": 22, "FI": 25, "FR": 20,
	"DE": 19, "GR": 24, "HU": 27, "IE": 23, "IT": 22,
	"LV": 21, "LT": 21, "LU": 17, "MT": 18, "NL": 21,
	"PT": 23, "RO": 19, "SK": 23, "SI": 22, "ES": 21,
	"SE": 25,
}

// resolveGoodsVatCode determines the correct VAT code for invoice line items.
// The company is registered under the EU OSS (One-Stop Shop) scheme, so the site
// calculates the destination-country VAT rate and we pass it through to wfirma.
//
// When taxRate is 0 (caller didn't provide tax_value), the rate is inferred:
//   - PL or unknown country → 23% (Polish standard rate)
//   - EU B2C → destination-country rate from VATProvider or defaultEURates
//   - Non-EU / EU B2B → handled by WDT/EXP codes (rate irrelevant)
//
// B2B rules (customer_group -1 for API callers, or OpenCart groups 6, 7, 16, 18, 19):
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
		if taxRate == 0 {
			return "23"
		}
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
	// B2C: destination-country rate, TaxID irrelevant.
	// When taxRate is 0 (no tax_value provided), look up the standard rate.
	if taxRate == 0 {
		if vp != nil {
			if rate := vp.GetStandardRate(countryCode); rate > 0 {
				return strconv.Itoa(int(rate))
			}
		}
		if rate, ok := defaultEURates[countryCode]; ok {
			return strconv.Itoa(rate)
		}
		return "23"
	}
	return strconv.Itoa(taxRate)
}

// formatCustomerGroup returns a human-readable label like "3 (B2C)" or "6 (B2B)".
func formatCustomerGroup(group int) string {
	if b2bCustomerGroups[group] {
		return fmt.Sprintf("%d (B2B)", group)
	}
	return fmt.Sprintf("%d (B2C)", group)
}
