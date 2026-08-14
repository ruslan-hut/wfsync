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

// wFirma contractor tax_id_type values. The type decides how wFirma identifies the
// buyer in the KSeF XML: "nip" emits Podmiot2/DaneIdentyfikacyjne/NIP, "custom"
// emits KodUE + NrVatUE (which is why a custom id without a resolvable EU country
// prefix produces an empty KodUE and a KSeF XML rejection), "none" emits BrakID.
const (
	taxIdTypeNone   = "none"
	taxIdTypeNip    = "nip"
	taxIdTypeCustom = "custom"
)

// resolveTaxId turns a buyer's raw tax ID into the (nip, tax_id_type) pair to send
// to wFirma, and a reason string when the ID had to be dropped.
//
// The pair matters beyond wFirma's own validation: wFirma builds the KSeF FA(2)
// buyer identification from it. "custom" means "not a NIP", so wFirma exports the
// value as an EU VAT number (KodUE + NrVatUE) and derives KodUE from the country
// prefix on the number itself. A bare national number stored as "custom" therefore
// yields an empty KodUE and KSeF rejects the XML with "Pole KodUE posiada
// niepoprawną wartość". Hence: a Polish NIP goes out as taxIdTypeNip with bare
// digits, a foreign EU number as taxIdTypeCustom with its prefix guaranteed, and
// anything that fits neither is dropped (taxIdTypeNone) rather than shipped as an
// identifier wFirma cannot map to a country.
//
// countryCode is the buyer's address country (ISO alpha-2) and is used only when
// the tax ID carries no prefix of its own.
func resolveTaxId(countryCode, taxId string) (nip, taxIdType, reason string) {
	// Strip separators so wFirma receives a compact number (e.g. "DE 362-155" → "DE362155").
	taxId = strings.ToUpper(strings.NewReplacer(" ", "", "-", "", ".", "").Replace(strings.TrimSpace(taxId)))
	if taxId == "" {
		return "", taxIdTypeNone, ""
	}

	// A prefix on the number wins over the address country: a buyer registered for
	// VAT abroad may well have a billing address elsewhere.
	prefix, rest := splitVatPrefix(taxId)
	if prefix == "" {
		rest = taxId
		prefix = vatPrefixForCountry(strings.ToUpper(strings.TrimSpace(countryCode)))
	}

	switch {
	case prefix == "PL":
		if !isValidPolishNIP(rest) {
			return "", taxIdTypeNone, "not a valid Polish NIP (10 digits + checksum)"
		}
		return rest, taxIdTypeNip, ""
	case prefix != "":
		return prefix + rest, taxIdTypeCustom, ""
	default:
		return "", taxIdTypeNone, "no EU country prefix on the tax ID and no EU billing country to derive one from"
	}
}

// splitVatPrefix separates a leading EU VAT country prefix from the rest of the
// number. It returns an empty prefix when the number does not start with a known
// one, so a national number that happens to begin with two letters is not mistaken
// for a prefixed VAT-UE number.
func splitVatPrefix(taxId string) (prefix, rest string) {
	if len(taxId) < 3 {
		return "", taxId
	}
	if p := taxId[:2]; euVatNumberPrefixes[p] {
		return p, taxId[2:]
	}
	return "", taxId
}

// vatPrefixForCountry returns the EU VAT prefix for an ISO country code, or "" for
// a non-EU country. Poland maps to itself even though euCountries excludes it —
// this is about identification, not about foreign VAT rates.
func vatPrefixForCountry(countryCode string) string {
	if countryCode == "PL" {
		return "PL"
	}
	if !euCountries[countryCode] {
		return ""
	}
	if alt, ok := euVatPrefixes[countryCode]; ok {
		return alt
	}
	return countryCode
}

// euVatNumberPrefixes is the set of prefixes that may legitimately open a VAT-UE
// number: the EU country codes, Poland, "EL" for Greece and "XI" for Northern
// Ireland. It matches the value list KSeF accepts for the KodUE field.
var euVatNumberPrefixes = buildEUVatNumberPrefixes()

func buildEUVatNumberPrefixes() map[string]bool {
	m := map[string]bool{"PL": true, "XI": true}
	for code := range euCountries {
		if alt, ok := euVatPrefixes[code]; ok {
			m[alt] = true
			continue
		}
		m[code] = true
	}
	return m
}

// isValidPolishNIP reports whether s is a syntactically valid Polish NIP: exactly
// ten digits whose weighted checksum matches the last one. Buyers regularly mistype
// it (a nine-digit value is the common case) and wFirma accepts the bad number as a
// "custom" identifier, so the check has to happen here — the rejection only surfaces
// later, as an unhelpful KSeF XML error.
func isValidPolishNIP(s string) bool {
	if len(s) != 10 {
		return false
	}
	weights := [9]int{6, 5, 7, 2, 3, 4, 5, 6, 7}
	sum := 0
	for i := 0; i < 9; i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
		sum += int(s[i]-'0') * weights[i]
	}
	if s[9] < '0' || s[9] > '9' {
		return false
	}
	check := sum % 11
	if check == 10 {
		return false
	}
	return check == int(s[9]-'0')
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
