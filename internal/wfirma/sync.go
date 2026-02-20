package wfirma

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"wfsync/entity"
	"wfsync/lib/sl"
)

// findInvoices fetches all invoices from wFirma matching a date range and type.
// Paginates through results with 100 items per page.
func (c *Client) findInvoices(ctx context.Context, from, to string, invType invoiceType) ([]InvoiceData, error) {
	const pageSize = 100
	var all []InvoiceData

	for page := 0; ; page++ {
		payload := map[string]interface{}{
			"api": map[string]interface{}{
				"invoices": map[string]interface{}{
					"parameters": map[string]interface{}{
						"limit": pageSize,
						"page":  page,
						"conditions": map[string]interface{}{
							"and": []map[string]interface{}{
								{
									"condition": map[string]interface{}{
										"field":    "date",
										"operator": "ge",
										"value":    from,
									},
								},
								{
									"condition": map[string]interface{}{
										"field":    "date",
										"operator": "le",
										"value":    to,
									},
								},
								{
									"condition": map[string]interface{}{
										"field":    "type",
										"operator": "eq",
										"value":    string(invType),
									},
								},
							},
						},
					},
				},
			},
		}

		res, err := c.request(ctx, "invoices", "find", payload)
		if err != nil {
			return nil, fmt.Errorf("find invoices page %d: %w", page, err)
		}

		var findResp InvoiceFindResponse
		if err = json.Unmarshal(res, &findResp); err != nil {
			return nil, fmt.Errorf("parse find response: %w", err)
		}
		if findResp.Status.Code == "ERROR" {
			return nil, fmt.Errorf("find invoices: API error")
		}

		for _, wrapper := range findResp.Invoices {
			all = append(all, wrapper.Invoice)
		}

		// Stop if we've fetched all results
		fetched := (page + 1) * pageSize
		if fetched >= findResp.Parameters.Total || len(findResp.Invoices) == 0 {
			break
		}
	}

	return all, nil
}

// SyncFromRemote pulls invoices from wFirma for the given date range and syncs them to local DB.
// Flow: fetch remote normal invoices, upsert each locally (with number), delete local records
// whose IDs are absent from the remote set.
func (c *Client) SyncFromRemote(ctx context.Context, from, to string) (*entity.SyncResult, error) {
	if !c.enabled {
		return nil, fmt.Errorf("wFirma is disabled")
	}
	if c.db == nil {
		return nil, fmt.Errorf("database not connected")
	}
	log := c.log.With(slog.String("op", "sync_from_remote"), slog.String("from", from), slog.String("to", to))

	// Fetch remote invoices
	remoteInvoices, err := c.findInvoices(ctx, from, to, invoiceNormal)
	if err != nil {
		return nil, fmt.Errorf("find remote invoices: %w", err)
	}

	result := &entity.SyncResult{
		RemoteCount: len(remoteInvoices),
	}

	// Build a set of remote IDs and upsert each invoice locally
	remoteIDs := make(map[string]bool, len(remoteInvoices))
	for _, inv := range remoteInvoices {
		remoteIDs[inv.Id] = true
		// Build an Invoice struct to save via existing SaveInvoice (upsert)
		localInv := &Invoice{
			Id:     inv.Id,
			Number: inv.Number,
			Type:   inv.Type,
			Date:   inv.Date,
		}
		if err = c.db.SaveInvoice(inv.Id, localInv); err != nil {
			log.Warn("upsert invoice", slog.String("id", inv.Id), sl.Err(err))
			continue
		}
		result.Upserted++
	}

	// Get local invoices for the same range
	localInvoices, err := c.db.GetInvoicesByDateRange(from, to, string(invoiceNormal))
	if err != nil {
		return nil, fmt.Errorf("get local invoices: %w", err)
	}
	result.LocalCount = len(localInvoices)

	// Delete local records absent from remote
	for _, local := range localInvoices {
		if !remoteIDs[local.Id] {
			if err = c.db.DeleteInvoiceById(local.Id); err != nil {
				log.Warn("delete orphaned invoice", slog.String("id", local.Id), sl.Err(err))
				continue
			}
			result.Deleted++
		}
	}

	log.With(
		slog.Int("remote", result.RemoteCount),
		slog.Int("local", result.LocalCount),
		slog.Int("upserted", result.Upserted),
		slog.Int("deleted", result.Deleted),
	).Info("sync from remote completed")

	return result, nil
}

// SyncToRemote pushes locally stored invoices to wFirma for the given date range.
// Flow: read local invoices, fetch remote invoices for the same range, find local IDs
// absent from remote, re-create each via invoices/add, replace old local record with new ID/number.
func (c *Client) SyncToRemote(ctx context.Context, from, to string) (*entity.SyncResult, error) {
	if !c.enabled {
		return nil, fmt.Errorf("wFirma is disabled")
	}
	if c.db == nil {
		return nil, fmt.Errorf("database not connected")
	}
	log := c.log.With(slog.String("op", "sync_to_remote"), slog.String("from", from), slog.String("to", to))

	// Get local invoices
	localInvoices, err := c.db.GetInvoicesByDateRange(from, to, string(invoiceNormal))
	if err != nil {
		return nil, fmt.Errorf("get local invoices: %w", err)
	}

	// Fetch remote invoices for the same range
	remoteInvoices, err := c.findInvoices(ctx, from, to, invoiceNormal)
	if err != nil {
		return nil, fmt.Errorf("find remote invoices: %w", err)
	}

	result := &entity.SyncResult{
		LocalCount:  len(localInvoices),
		RemoteCount: len(remoteInvoices),
	}

	// Build a set of remote IDs
	remoteIDs := make(map[string]bool, len(remoteInvoices))
	for _, inv := range remoteInvoices {
		remoteIDs[inv.Id] = true
	}

	// Re-create local invoices that are missing on remote
	for _, local := range localInvoices {
		if remoteIDs[local.Id] {
			continue
		}

		newId, newNumber, err := c.recreateInvoice(ctx, local)
		if err != nil {
			log.Warn("recreate invoice",
				slog.String("old_id", local.Id),
				sl.Err(err))
			continue
		}

		// Delete old local record
		if err = c.db.DeleteInvoiceById(local.Id); err != nil {
			log.Warn("delete old local invoice", slog.String("id", local.Id), sl.Err(err))
		}

		// Save new record with updated ID and number
		local.Id = newId
		local.Number = newNumber
		if err = c.db.SaveInvoice(newId, local); err != nil {
			log.Warn("save recreated invoice", slog.String("id", newId), sl.Err(err))
		}

		result.Recreated++
		log.Info("invoice recreated",
			slog.String("old_id", local.Id),
			slog.String("new_id", newId),
			slog.String("new_number", newNumber))
	}

	log.With(
		slog.Int("local", result.LocalCount),
		slog.Int("remote", result.RemoteCount),
		slog.Int("recreated", result.Recreated),
	).Info("sync to remote completed")

	return result, nil
}

// recreateInvoice posts an invoices/add request to wFirma using stored LocalInvoice data.
// Returns the new invoice ID and number assigned by the API.
func (c *Client) recreateInvoice(ctx context.Context, local *entity.LocalInvoice) (string, string, error) {
	// Build contractor reference
	var contractor *Contractor
	if local.Contractor != nil {
		contractor = &Contractor{ID: local.Contractor.ID}
	}

	// Build content lines
	var contents []*ContentLine
	for _, line := range local.Contents {
		if line.Content == nil {
			continue
		}
		content := &Content{
			Name:  line.Content.Name,
			Count: line.Content.Count,
			Price: line.Content.Price,
			Unit:  line.Content.Unit,
		}
		if line.Content.Good != nil {
			content.Good = &GoodRef{ID: line.Content.Good.ID}
		}
		// Preserve vat_code reference from stored data; for old records that only
		// have a vat string, resolve it through setContentVat.
		if line.Content.VatCode != nil && line.Content.VatCode.ID > 0 {
			content.VatCode = &VatCodeRef{ID: line.Content.VatCode.ID}
		} else {
			c.setContentVat(ctx, content, line.Content.Vat)
		}
		contents = append(contents, &ContentLine{Content: content})
	}

	invoice := &Invoice{
		Contractor:    contractor,
		Type:          local.Type,
		PriceType:     local.PriceType,
		PaymentMethod: local.PaymentMethod,
		PaymentDate:   local.PaymentDate,
		DisposalDate:  local.DisposalDate,
		Total:         local.Total,
		IdExternal:    local.IdExternal,
		Description:   local.Description,
		Date:          local.Date,
		Currency:      local.Currency,
		Contents:      contents,
	}

	payload := map[string]interface{}{
		"api": map[string]interface{}{
			"invoices": []map[string]interface{}{
				{
					"invoice": invoice,
				},
			},
		},
	}

	res, err := c.request(ctx, "invoices", "add", payload)
	if err != nil {
		return "", "", fmt.Errorf("add invoice: %w", err)
	}

	var addResp InvoiceResponse
	if err = json.Unmarshal(res, &addResp); err != nil {
		return "", "", fmt.Errorf("parse add response: %w", err)
	}

	var resultInvoice InvoiceData
	if wrapper, ok := addResp.Invoices["0"]; ok {
		resultInvoice = wrapper.Invoice
	}
	if resultInvoice.Id == "" {
		return "", "", fmt.Errorf("no invoice id returned from wFirma")
	}

	return resultInvoice.Id, resultInvoice.Number, nil
}
