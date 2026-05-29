package entity

import "time"

// VIESResult is the outcome of a VIES VAT number validation check.
//
// VIES distinguishes a definitive verdict (the number is valid or invalid) from a
// service/transient failure (e.g. MS_MAX_CONCURRENT_REQ, MS_UNAVAILABLE) where the
// check could not be completed. Only definitive results are meaningful and cacheable;
// an inconclusive result must never be treated as "invalid" nor persisted.
type VIESResult int

const (
	// VIESInconclusive means VIES could not complete the check (service or transient
	// error). The number is neither confirmed valid nor invalid.
	VIESInconclusive VIESResult = iota
	// VIESValid means VIES confirmed the number is valid.
	VIESValid
	// VIESInvalid means VIES confirmed the number is not valid.
	VIESInvalid
)

// VIESValidation stores the result of a VIES VAT number validation check.
// Cached in MongoDB with a composite key of country_code + vat_number.
type VIESValidation struct {
	CountryCode       string    `json:"country_code" bson:"country_code"`
	VATNumber         string    `json:"vat_number" bson:"vat_number"`
	RequestDate       string    `json:"request_date" bson:"request_date"`
	Valid             bool      `json:"valid" bson:"valid"`
	Name              string    `json:"name,omitempty" bson:"name,omitempty"`
	Address           string    `json:"address,omitempty" bson:"address,omitempty"`
	RequestIdentifier string    `json:"request_identifier,omitempty" bson:"request_identifier,omitempty"`
	ValidatedAt       time.Time `json:"validated_at" bson:"validated_at"`
}
