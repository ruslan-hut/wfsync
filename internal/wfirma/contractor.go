package wfirma

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"wfsync/entity"
	"wfsync/lib/sl"
)

// createContractor registers a new contractor in wFirma and returns its ID.
//
// wFirma mandatory fields: name, zip, city (API returns validation error if any is empty).
// The function defaults name to "Kontrahent <email>", zip to "01-001", city to "Warszawa".
//
// Optional fields sent: email, country (ISO 3166 alpha-2), street, nip, tax_id_type.
// tax_id_type: "none" = no tax ID provided, "custom" = tax ID present in the nip field.
// Using "none"/"custom" (instead of "other") allows wFirma to accept custom VAT rates on invoices.
func (c *Client) createContractor(ctx context.Context, customer *entity.ClientDetails) (string, error) {
	if customer == nil {
		return "", fmt.Errorf("no customer")
	}
	if customer.Name == "" {
		customer.Name = "Kontrahent " + customer.Email
	}
	if customer.ZipCode == "" {
		customer.ZipCode = "01-001"
	}
	if customer.City == "" {
		customer.City = "Warszawa"
	}
	taxIdType := "none"
	if customer.TaxId != "" {
		taxIdType = "custom"
	}

	countryCode := customer.CountryCode()
	if countryCode == "PL" {
		customer.ZipCode = customer.NormalizeZipCode()
	}
	// Foreign EU buyers need the country prefix on their VAT-UE number or wFirma
	// rejects 0% WDT / EU reverse-charge invoices (contractor.nip validation).
	nip := normalizeEUVatNumber(countryCode, customer.TaxId)

	// If not found, create a new contractor.
	payload := map[string]interface{}{
		"api": map[string]interface{}{
			"contractors": []map[string]interface{}{
				{
					"contractor": map[string]interface{}{
						"name":        customer.Name,
						"email":       customer.Email,
						"country":     countryCode,
						"zip":         customer.ZipCode,
						"city":        customer.City,
						"street":      customer.Street,
						"tax_id_type": taxIdType,
						"nip":         nip,
					},
				},
			},
		},
	}
	createRes, err := c.request(ctx, "contractors", "add", payload)
	if err != nil {
		c.log.Error("create contractor",
			slog.String("email", customer.Email),
			sl.Err(err))
		return "", err
	}
	var addResp Response
	if err = json.Unmarshal(createRes, &addResp); err != nil {
		c.log.Error("parse contractor creation response", sl.Err(err))
		return "", err
	}
	contr := addResp.Contractors["0"].Contractor
	if addResp.Status.Code == "ERROR" {
		if len(contr.ErrorsRaw) > 0 {
			for _, w := range contr.ErrorsRaw { // берём первый элемент мапы
				c.log.With(
					slog.String("field", w.Error.Field),
					slog.String("message", w.Error.Message),
					slog.String("method", w.Error.Method.Name),
					slog.String("parameters", w.Error.Method.Parameters),
					slog.String("email", customer.Email),
					slog.String("name", customer.Name),
					slog.String("tg_topic", entity.TopicError),
				).Error("add contractor")
				break
			}
		}
		return "", fmt.Errorf("no contractor id returned")
	}
	if contr.ID == "" {
		c.log.Error("no contractor ID returned from wFirma", slog.String("response", string(createRes)))
		return "", fmt.Errorf("no contractor id returned")
	}
	c.log.Debug("new contractor created",
		slog.String("email", customer.Email),
		slog.String("name", customer.Name),
		slog.String("contractorID", contr.ID))
	return contr.ID, nil
}

// syncContractor refreshes an existing wFirma contractor from the current order.
//
// wFirma prints the invoice header from the contractor record, not from the invoice
// payload, so a customer who moved would keep receiving invoices with their old
// address unless the record is refreshed before the document is issued.
//
// Empty request fields never blank out stored values, and the edit call is skipped
// entirely when nothing differs.
func (c *Client) syncContractor(ctx context.Context, stored *Contractor, customer *entity.ClientDetails) error {
	if stored == nil || customer == nil {
		return nil
	}

	countryCode := customer.CountryCode()
	zip := customer.ZipCode
	if countryCode == "PL" && zip != "" {
		zip = customer.NormalizeZipCode()
	}
	// Foreign EU buyers need the country prefix on their VAT-UE number or wFirma
	// rejects 0% WDT / EU reverse-charge invoices (contractor.nip validation).
	nip := normalizeEUVatNumber(countryCode, customer.TaxId)

	fields := map[string]string{
		"name":    firstNonEmpty(customer.Name, stored.Name),
		"country": firstNonEmpty(countryCode, stored.Country),
		"zip":     firstNonEmpty(zip, stored.Zip),
		"city":    firstNonEmpty(customer.City, stored.City),
		"street":  firstNonEmpty(customer.Street, stored.Street),
		"nip":     firstNonEmpty(nip, stored.Nip),
	}
	current := map[string]string{
		"name":    stored.Name,
		"country": stored.Country,
		"zip":     stored.Zip,
		"city":    stored.City,
		"street":  stored.Street,
		"nip":     stored.Nip,
	}

	var changed []string
	for field, value := range fields {
		if !strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(current[field])) {
			changed = append(changed, field)
		}
	}
	if len(changed) == 0 {
		return nil
	}

	contractor := map[string]interface{}{"id": stored.ID}
	for field, value := range fields {
		contractor[field] = value
	}
	// tax_id_type: "none" = no tax ID, "custom" = tax ID present in the nip field.
	contractor["tax_id_type"] = "none"
	if fields["nip"] != "" {
		contractor["tax_id_type"] = "custom"
	}

	payload := map[string]interface{}{
		"api": map[string]interface{}{
			"contractors": []map[string]interface{}{
				{"contractor": contractor},
			},
		},
	}

	res, err := c.request(ctx, "contractors", "edit/"+stored.ID, payload)
	if err != nil {
		return fmt.Errorf("edit contractor: %w", err)
	}

	var editResp Response
	if err = json.Unmarshal(res, &editResp); err != nil {
		return fmt.Errorf("parse contractor edit response: %w", err)
	}
	if editResp.Status.Code == "ERROR" {
		contr := editResp.Contractors["0"].Contractor
		for _, w := range contr.ErrorsRaw {
			return fmt.Errorf("edit contractor: %s: %s", w.Error.Field, w.Error.Message)
		}
		return fmt.Errorf("edit contractor: unknown error")
	}
	c.log.Info("contractor updated",
		slog.String("contractor_id", stored.ID),
		slog.String("email", customer.Email),
		slog.String("fields", strings.Join(changed, ",")))
	return nil
}

// firstNonEmpty returns the first non-empty value, ignoring surrounding whitespace.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// getContractor searches for an existing contractor by email.
// Returns the stored wFirma record, or nil when no match is found.
//
// The whole record is returned (not just the ID) so the invoice flow can promote
// returning customers to B2B from the stored NIP and refresh a stale address before
// issuing the document.
func (c *Client) getContractor(ctx context.Context, email string) (*Contractor, error) {
	if email == "" {
		return nil, nil
	}
	log := c.log.With(slog.String("email", email))

	// Try to find by customer email first.
	search := map[string]interface{}{
		"api": map[string]interface{}{
			"contractors": map[string]interface{}{
				"parameters": map[string]interface{}{
					"conditions": []map[string]interface{}{
						{
							"condition": map[string]interface{}{
								"field":    "email",
								"operator": "eq",
								"value":    email,
							},
						},
					},
				},
			},
		},
	}

	res, err := c.request(ctx, "contractors", "find", search)
	if err == nil {
		var findResp struct {
			Contractors struct {
				Element0 struct {
					Contractor Contractor `json:"contractor"`
				} `json:"0"`
			} `json:"contractors"`
		}
		if err := json.Unmarshal(res, &findResp); err != nil {
			log.Warn("parse contractor find response", sl.Err(err))
		}
		if found := findResp.Contractors.Element0.Contractor; found.ID != "" {
			found.Nip = strings.TrimSpace(found.Nip)
			log.Debug("found existing contractor",
				slog.String("contractor_id", found.ID),
				slog.String("nip", found.Nip))
			return &found, nil
		}
	} else {
		log.Warn("searching for contractor", sl.Err(err))
	}

	return nil, nil
}
