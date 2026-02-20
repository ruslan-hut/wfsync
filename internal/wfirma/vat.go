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
