package entity

import "time"

// VATRate represents a persisted EU VAT standard rate for a single country.
type VATRate struct {
	CountryCode  string    `json:"country_code" bson:"country_code"`
	StandardRate float64   `json:"standard_rate" bson:"standard_rate"`
	CountryName  string    `json:"country_name" bson:"country_name"`
	UpdatedAt    time.Time `json:"updated_at" bson:"updated_at"`
}

// StandardVATRates is the curated source of truth for EU standard VAT rates
// (ISO 3166-1 alpha-2 → percent), excluding Poland which is handled separately
// in the invoice logic. Because the rate determines what customers are legally
// charged, this map is deliberately code-reviewed and auditable rather than
// trusted blindly from an external feed.
//
// Dynamic sources (the vatlookup.eu API and the MongoDB cache) are reconciled
// against this baseline — see internal/vatrates. When a dynamic source drifts
// from these values it is corrected and flagged, which is what guards against a
// stale external feed (e.g. vatlookup.eu still reporting Slovakia at the
// pre-2025 20% rather than the 23% in force since 2025-01-01).
//
// MAINTENANCE: update an entry whenever an EU member changes its standard rate.
var StandardVATRates = map[string]float64{
	"AT": 20, "BE": 21, "BG": 20, "HR": 25, "CY": 19,
	"CZ": 21, "DK": 25, "EE": 22, "FI": 25, "FR": 20,
	"DE": 19, "GR": 24, "HU": 27, "IE": 23, "IT": 22,
	"LV": 21, "LT": 21, "LU": 17, "MT": 18, "NL": 21,
	"PT": 23, "RO": 19, "SK": 23, "SI": 22, "ES": 21,
	"SE": 25,
}

// VAT rate plausibility band for dynamically-sourced rates. EU law sets a 15%
// floor on the standard rate; no member currently exceeds Hungary's 27%. A rate
// from an external source outside this band is treated as bad data.
const (
	MinStandardVATRate = 15.0
	MaxStandardVATRate = 27.0
)
