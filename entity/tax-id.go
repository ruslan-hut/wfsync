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

// IsWellFormedTaxId reports whether a tax ID, once spaces, hyphens and dots are
// removed, is non-empty and made only of Latin letters and digits — the alphabet of
// every EU VAT number. It checks the form, not validity.
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
