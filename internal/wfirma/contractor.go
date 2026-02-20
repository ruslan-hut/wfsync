package wfirma

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"wfsync/entity"
	"wfsync/lib/sl"
)

// createContractor registers a new contractor in wFirma and returns its ID.
// Contractor fields: name, email, country (ISO 3166 alpha-2), zip, city, street, nip, tax_id_type.
// tax_id_type: "other" = no tax ID provided, "custom" = tax ID present.
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
	taxIdType := "other"
	if customer.TaxId != "" {
		taxIdType = "custom"
	}

	countryCode := customer.CountryCode()
	if countryCode == "PL" {
		customer.ZipCode = customer.NormalizeZipCode()
	}

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
						"nip":         customer.TaxId,
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
		c.log.Error("no contractor ID returned from wFirma", slog.Any("error", createRes))
		return "", fmt.Errorf("no contractor id returned")
	}
	c.log.Debug("new contractor created",
		slog.String("email", customer.Email),
		slog.String("name", customer.Name),
		slog.String("contractorID", contr.ID))
	return contr.ID, nil
}

// getContractor searches for an existing contractor by email. Returns empty string if not found.
func (c *Client) getContractor(ctx context.Context, email string) (string, error) {
	if email == "" {
		return "", nil
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
					Contractor struct {
						ID string `json:"id"`
					} `json:"contractor"`
				} `json:"0"`
			} `json:"contractors"`
		}
		_ = json.Unmarshal(res, &findResp)
		if findResp.Contractors.Element0.Contractor.ID != "" {
			contractorID := findResp.Contractors.Element0.Contractor.ID
			log.Debug("found existing contractor",
				slog.String("contractor_id", contractorID))
			return contractorID, nil
		}
	} else {
		log.Warn("searching for contractor", sl.Err(err))
	}

	return "", nil
}
