package entity

import "time"

// VATRate represents a persisted EU VAT standard rate for a single country.
type VATRate struct {
	CountryCode  string    `json:"country_code" bson:"country_code"`
	StandardRate float64   `json:"standard_rate" bson:"standard_rate"`
	CountryName  string    `json:"country_name" bson:"country_name"`
	UpdatedAt    time.Time `json:"updated_at" bson:"updated_at"`
}
