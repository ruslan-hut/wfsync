package wfirma

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"wfsync/entity"
	"wfsync/lib/sl"
)

// findInvoices fetches all invoices from wFirma matching a date range and type.
// Paginates through results with 100 items per page.
//
// wFirma pages are 1-based, and the page metadata for invoices/find is returned inside
// the invoices map (as a "parameters" entry) rather than at the top level — so the total
// is not readable from the decoded response. Pagination therefore stops when a page comes
// back short, the same approach as the vat_codes cache. maxPages bounds the loop should
// the API ever keep returning full pages.
func (c *Client) findInvoices(ctx context.Context, from, to string, invType invoiceType) ([]InvoiceData, error) {
	const (
		pageSize = 100
		maxPages = 200
	)
	var all []InvoiceData

	for page := 1; page <= maxPages; page++ {
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

		// The map carries a non-invoice "parameters" entry alongside the numbered ones;
		// it decodes to an empty InvoiceData, so skip anything without an id.
		found := 0
		for _, wrapper := range findResp.Invoices {
			if wrapper.Invoice.Id == "" {
				continue
			}
			all = append(all, wrapper.Invoice)
			found++
		}

		// A short page is the last page.
		if found < pageSize {
			return all, nil
		}
		if page == maxPages {
			c.log.With(
				slog.String("from", from),
				slog.String("to", to),
				slog.String("type", string(invType)),
				slog.Int("fetched", len(all)),
			).Warn("invoice pagination hit page limit, results may be incomplete")
		}
	}

	return all, nil
}

// FindInvoices returns every faktura registered in wFirma within a date range —
// both accepted invoices (normal) and drafts (normal_draft). Drafts are included
// because the KSeF fallback registers a real order as a draft (see submitInvoice),
// so omitting them would report those orders as never invoiced. The type is carried
// on each item so callers can tell the two apart.
//
// The API filters type with an equality condition, so each type is fetched in its own
// paginated query and the results concatenated.
// Converts API response data to entity.LocalInvoice to avoid leaking internal types.
func (c *Client) FindInvoices(ctx context.Context, from, to string) ([]*entity.LocalInvoice, error) {
	if !c.enabled {
		return nil, fmt.Errorf("wFirma is disabled")
	}
	var result []*entity.LocalInvoice
	for _, invType := range []invoiceType{invoiceNormal, invoiceNormalDraft} {
		data, err := c.findInvoices(ctx, from, to, invType)
		if err != nil {
			return nil, fmt.Errorf("find %s invoices: %w", invType, err)
		}
		for _, inv := range data {
			result = append(result, toLocalInvoice(inv))
		}
	}
	return result, nil
}

// toLocalInvoice converts an API invoice record to the transport-neutral entity form.
func toLocalInvoice(inv InvoiceData) *entity.LocalInvoice {
	li := &entity.LocalInvoice{
		Id:          inv.Id,
		Number:      inv.Number,
		Type:        inv.Type,
		Date:        inv.Date,
		Currency:    inv.Currency,
		IdExternal:  inv.IdExternal,
		Description: inv.Description,
	}
	if t, err := strconv.ParseFloat(inv.Total, 64); err == nil {
		li.Total = t
	}
	if inv.Contractor != nil {
		li.Contractor = &entity.LocalContractor{
			ID:   inv.Contractor.ID,
			Name: inv.Contractor.Name,
		}
	}
	return li
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
			log.Warn("upsert invoice", slog.String("invoice_id", inv.Id), sl.Err(err))
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
				log.Warn("delete orphaned invoice", slog.String("invoice_id", local.Id), sl.Err(err))
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

		oldId := local.Id
		newId, newNumber, err := c.recreateInvoice(ctx, local)
		if err != nil {
			log.Warn("recreate invoice",
				slog.String("old_invoice_id", oldId),
				sl.Err(err))
			continue
		}

		// Delete old local record
		if err = c.db.DeleteInvoiceById(oldId); err != nil {
			log.Warn("delete old local invoice", slog.String("invoice_id", oldId), sl.Err(err))
		}

		// Save new record with updated ID and number
		local.Id = newId
		local.Number = newNumber
		if err = c.db.SaveInvoice(newId, local); err != nil {
			log.Warn("save recreated invoice", slog.String("invoice_id", newId), sl.Err(err))
		}

		result.Recreated++
		log.Info("invoice recreated",
			slog.String("old_invoice_id", oldId),
			slog.String("new_invoice_id", newId),
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
			Vat:   line.Content.Vat,
		}
		if line.Content.Good != nil {
			content.Good = &GoodRef{ID: line.Content.Good.ID}
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
