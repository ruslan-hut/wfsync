package entity

import "time"

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
