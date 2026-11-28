package entity

import (
	"errors"
	"strings"
)

// ErrInvalidTaxId signals a buyer VAT number that cannot be a VAT number at all, e.g.
// one typed with a Cyrillic letter that looks Latin. Handlers surface it as a 400:
// the number must be corrected at the source, never repaired here.
var ErrInvalidTaxId = errors.New("invalid vat number")

// taxIdSeparators are the characters buyers type into VAT numbers that carry no meaning.
var taxIdSeparators = strings.NewReplacer(" ", "", "-", "", ".", "")

// NormalizeTaxId upper-cases a tax ID and strips the separators buyers type into it
// ("CHE-266.473.703" → "CHE266473703"). It does not validate the result.
func NormalizeTaxId(taxId string) string {
	return strings.ToUpper(taxIdSeparators.Replace(strings.TrimSpace(taxId)))
}

// IsWellFormedTaxId reports whether a tax ID, once separators are removed, is non-empty
// and made only of Latin letters and digits — the alphabet of every EU VAT number. It
// checks the form, not validity.
func IsWellFormedTaxId(taxId string) bool {
	s := taxIdSeparators.Replace(strings.TrimSpace(taxId))
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		b := s[i]
		if !(b >= '0' && b <= '9' || b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z') {
			return false
		}
	}
	return true
}

// euVatPrefixes is the set of prefixes that may open an EU VAT (VAT-UE) number: the EU
// country codes, Poland, "EL" for Greece and "XI" for Northern Ireland. It matches the
// country list VIES serves and the value list KSeF accepts for KodUE.
var euVatPrefixes = buildEUVatPrefixes()

func buildEUVatPrefixes() map[string]bool {
	m := map[string]bool{"PL": true, "XI": true}
	for code := range StandardVATRates {
		m[VATPrefixForCountry(code)] = true
	}
	return m
}

// IsEUVatPrefix reports whether prefix opens an EU VAT number. A two-letter code that
// is not one (CH, GB, NO, ...) belongs to a non-EU tax system that VIES does not cover.
func IsEUVatPrefix(prefix string) bool {
	return euVatPrefixes[prefix]
}

// VATPrefixForCountry maps an ISO 3166 alpha-2 country code to its VAT number prefix.
// The only EU code that differs is Greece: country "GR", VAT prefix "EL".
func VATPrefixForCountry(countryCode string) string {
	if countryCode == "GR" {
		return "EL"
	}
	return countryCode
}
