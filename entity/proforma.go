package entity

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// ProformaRecord remembers the proforma set issued for an order so a repeated request
// with unchanged data returns the same documents instead of issuing new ones.
type ProformaRecord struct {
	ExternalId  string    `json:"external_id" bson:"external_id"`
	OrderId     string    `json:"order_id" bson:"order_id"`
	Fingerprint string    `json:"fingerprint" bson:"fingerprint"`
	Payment     *Payment  `json:"payment" bson:"payment"`
	Created     time.Time `json:"created" bson:"created"`
}

// DocumentIds returns the wFirma ids of every document in the record, in part order.
func (r *ProformaRecord) DocumentIds() []string {
	if r == nil || r.Payment == nil {
		return nil
	}
	if len(r.Payment.Parts) == 0 {
		return []string{r.Payment.Id}
	}
	ids := make([]string, 0, len(r.Payment.Parts))
	for _, part := range r.Payment.Parts {
		if part != nil {
			ids = append(ids, part.Id)
		}
	}
	return ids
}

// ProformaFingerprint hashes everything printed on a proforma: the order number, the
// client and its billing address, the currency, the amounts and every line in order.
// Line order is kept because it decides how a large order is split into parts.
func (c *CheckoutParams) ProformaFingerprint() string {
	var b strings.Builder
	field := func(v any) {
		_, _ = fmt.Fprintf(&b, "%v\x1f", v)
	}

	field(c.OrderId)
	if cd := c.ClientDetails; cd != nil {
		field(cd.Name)
		field(strings.ToLower(cd.Email))
		field(cd.Phone)
		field(cd.TaxId)
		field(cd.Country)
		field(cd.City)
		field(cd.Street)
		field(cd.ZipCode)
	}
	field(strings.ToUpper(c.Currency))
	field(c.Total)
	field(c.TaxValue)
	field(c.Shipping)
	for _, item := range c.LineItems {
		if item == nil {
			continue
		}
		field(item.Sku)
		field(item.Name)
		field(item.Qty)
		field(item.Price)
		field(item.Shipping)
	}

	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}
