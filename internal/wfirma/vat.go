package wfirma

import (
	"fmt"
	"strconv"
	"strings"
	"wfsync/entity"
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

// euCountries and defaultEURates are derived from the single curated source of
// truth (entity.StandardVATRates) so the compliance data lives in exactly one
// place and cannot drift between packages. Both are used as a fallback when the
// dynamic VATProvider is unavailable or unverified.
//
//   - euCountries: EU member state codes (excluding Poland), for membership checks.
//   - defaultEURates: standard rate per country, used as a last-resort rate when
//     the caller didn't provide tax_value.
var (
	euCountries    = buildEUCountries()
	defaultEURates = buildDefaultEURates()
)

func buildEUCountries() map[string]bool {
	m := make(map[string]bool, len(entity.StandardVATRates))
	for code := range entity.StandardVATRates {
		m[code] = true
	}
	return m
}

func buildDefaultEURates() map[string]int {
	m := make(map[string]int, len(entity.StandardVATRates))
	for code, rate := range entity.StandardVATRates {
		m[code] = int(rate)
	}
	return m
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

// ExpectedB2BVATRate returns the VAT rate percent that internal rules require for
// a B2B order shipped to countryCode, given whether the buyer supplied a VAT
// number. It mirrors resolveGoodsVatCode's B2B branch but yields a plain numeric
// percent (0 for the zero-rated WDT/EXP/exempt codes) so callers can validate a
// payload-declared rate before an invoice is created.
//
// The rules are deliberately independent of any rate the payload carries:
//   - PL or unknown country        → 23 (Polish standard rate)
//   - EU country with VAT number   → 0  (WDT, intra-community delivery)
//   - EU country without VAT number→ 23 (Polish rate, not the destination rate)
//   - Non-EU country               → 0  (EXP, export)
//
// Passing taxRate 0 into resolveGoodsVatCode forces the PL/unknown branch to the
// 23% default, which is exactly the rule we want B2B callers to be held to.
func ExpectedB2BVATRate(countryCode string, hasTaxId bool, vp VATProvider) int {
	code := resolveGoodsVatCode(0, countryCode, hasTaxId, true, vp)
	switch code {
	case vatWDT, vatEXP, vatNP, vatNPUE, vatZW:
		return 0
	}
	rate, _ := strconv.Atoi(code)
	return rate
}

// euVatPrefixes maps an ISO 3166 alpha-2 country code to its EU VAT-UE number
// prefix where it differs from the country code. The only such case is Greece,
// whose VAT numbers carry the "EL" prefix while its country code is "GR".
var euVatPrefixes = map[string]string{
	"GR": "EL",
}

// normalizeEUVatNumber ensures an EU contractor's tax ID carries the country
// prefix that wFirma requires for 0% WDT (intra-community delivery) and EU
// reverse-charge invoices. wFirma validates the buyer's VAT-UE number and
// rejects a bare national number (e.g. "28982711" instead of "CZ28982711") with
// "Nieprawidłowy prefiks kraju Unii Europejskiej".
//
// It is a no-op when the tax ID is empty, the country is not a foreign EU member
// (euCountries excludes Poland, so domestic NIPs are left untouched), or the tax
// ID already starts with a two-letter alphabetic prefix (assumed to be the
// country code already).
func normalizeEUVatNumber(countryCode, taxId string) string {
	taxId = strings.TrimSpace(taxId)
	if taxId == "" || !euCountries[countryCode] {
		return taxId
	}
	// Strip separators so wFirma receives a compact VAT-UE number (e.g. "DE 362-155" → "DE362155").
	taxId = strings.NewReplacer(" ", "", "-", "").Replace(taxId)
	isLetter := func(b byte) bool { return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') }
	if len(taxId) >= 2 && isLetter(taxId[0]) && isLetter(taxId[1]) {
		return taxId
	}
	prefix := countryCode
	if alt, ok := euVatPrefixes[countryCode]; ok {
		prefix = alt
	}
	return prefix + taxId
}

// IsB2BCustomerGroup returns true if the given customer group ID is a B2B group.
func IsB2BCustomerGroup(group int) bool {
	return b2bCustomerGroups[group]
}

// formatCustomerGroup returns a human-readable label like "3 (B2C)" or "6 (B2B)".
func formatCustomerGroup(group int) string {
	if b2bCustomerGroups[group] {
		return fmt.Sprintf("%d (B2B)", group)
	}
	return fmt.Sprintf("%d (B2C)", group)
}
