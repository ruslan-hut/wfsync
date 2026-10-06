package core

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"time"
	"wfsync/entity"
	"wfsync/lib/sl"
)

// ProformaDatabase remembers the proforma set issued for each B2B order.
type ProformaDatabase interface {
	GetProformaRecord(externalId string) (*entity.ProformaRecord, error)
	SaveProformaRecord(record *entity.ProformaRecord) error
	DeleteProformaRecord(externalId string) error
}

// SetProformaDatabase injects the proforma record store.
func (c *Core) SetProformaDatabase(db ProformaDatabase) {
	c.proformaDb = db
}

// B2BCreateProforma issues the proforma set for a B2B order, or returns the set already
// issued when nothing printed on it has changed.
//
// A large order is split into several documents, and issuing them can outlast the
// caller's HTTP timeout. The work is therefore detached from the request and serialized
// per order: a caller that gave up and retries waits for the running issuance and then
// receives its result, instead of issuing a second set. When the data did change, every
// proforma registered for the order is deleted before the new set is issued, so an order
// never carries more than one set.
func (c *Core) B2BCreateProforma(ctx context.Context, order *entity.B2BOrder) (*entity.Payment, error) {
	params := order.ToCheckoutParams()
	if err := c.validateB2BVATRate(params); err != nil {
		return nil, err
	}
	if c.inv == nil {
		return nil, fmt.Errorf("invoice service not connected")
	}

	ctx = context.WithoutCancel(ctx)
	ref := params.ExternalRef()
	defer c.proformaLocks.Lock(ref)()

	log := c.log.With(
		slog.String("order_id", params.OrderId),
		slog.String("external_id", ref),
	)
	fingerprint := params.ProformaFingerprint()

	registeredIds, err := c.inv.FindProformaIds(ctx, ref)
	if err != nil {
		// State unknown — issuing now could leave a second set beside the first.
		return nil, fmt.Errorf("look up existing proformas: %w", err)
	}

	record := c.proformaRecord(ref, log)
	log.With(
		slog.Any("registered_ids", registeredIds),
		slog.Any("recorded_ids", record.DocumentIds()),
		slog.Bool("fingerprint_match", record != nil && record.Fingerprint == fingerprint),
	).Debug("proforma state")
	if record != nil && record.Fingerprint == fingerprint && containsAll(registeredIds, record.DocumentIds()) {
		// Proformas registered beside the remembered set are copies no caller holds
		// (left by a run that never recorded them); removing them keeps one set per order
		// without renumbering the documents the client may already have.
		if extra := without(registeredIds, record.DocumentIds()); len(extra) > 0 {
			for _, id := range extra {
				if err := c.inv.DeleteProforma(ctx, id); err != nil {
					return nil, fmt.Errorf("delete duplicate proforma %s: %w", id, err)
				}
			}
			log.With(slog.Any("proforma_ids", extra)).Info("duplicate proformas deleted")
		}

		// The documents are valid and may already be with the client; a failed PDF
		// download is a reason to retry, never to replace them under new numbers.
		payment, err := c.reuseProforma(ctx, record.Payment)
		if err != nil {
			return nil, fmt.Errorf("reuse issued proforma: %w", err)
		}
		log.With(slog.Int("documents", len(registeredIds))).Info("proforma unchanged, reusing issued documents")
		return payment, nil
	}

	if err := c.discardProformas(ctx, ref, registeredIds, record, log); err != nil {
		return nil, err
	}

	payment, err := c.inv.RegisterProforma(ctx, params)
	if err != nil {
		return nil, err
	}

	fileName, link, err := c.downloadInvoice(ctx, "", payment.Id)
	if err != nil {
		return nil, err
	}
	payment.Link = link
	payment.InvoiceFile = fileName

	if err := c.downloadParts(ctx, payment); err != nil {
		return nil, err
	}

	if c.proformaDb != nil {
		err = c.proformaDb.SaveProformaRecord(&entity.ProformaRecord{
			ExternalId:  ref,
			OrderId:     params.OrderId,
			Fingerprint: fingerprint,
			Payment:     payment,
			Created:     time.Now(),
		})
		if err != nil {
			log.Error("save proforma record", sl.Err(err))
		}
	}

	return payment, nil
}

// proformaRecord returns the remembered proforma set for ref, nil when there is none or
// it cannot be read — either way the order is treated as having no reusable set.
func (c *Core) proformaRecord(ref string, log *slog.Logger) *entity.ProformaRecord {
	if c.proformaDb == nil || ref == "" {
		return nil
	}
	record, err := c.proformaDb.GetProformaRecord(ref)
	if err != nil {
		log.Warn("get proforma record", sl.Err(err))
		return nil
	}
	return record
}

// discardProformas deletes every proforma registered in wFirma for the order, then the
// local files and the record of the previous set. A failed wFirma delete aborts: issuing
// a new set beside a document that could not be removed is the duplicate this prevents.
func (c *Core) discardProformas(ctx context.Context, ref string, ids []string, record *entity.ProformaRecord, log *slog.Logger) error {
	for _, id := range ids {
		if err := c.inv.DeleteProforma(ctx, id); err != nil {
			return fmt.Errorf("delete stale proforma %s: %w", id, err)
		}
	}
	if len(ids) > 0 {
		log.With(slog.Any("proforma_ids", ids)).Info("stale proformas deleted")
	}

	if record == nil {
		return nil
	}
	for _, name := range proformaFiles(record.Payment) {
		if err := os.Remove(filepath.Join(c.filePath, name)); err != nil && !os.IsNotExist(err) {
			log.With(slog.String("file", name)).Warn("remove proforma file", sl.Err(err))
		}
	}
	if c.proformaDb != nil {
		if err := c.proformaDb.DeleteProformaRecord(ref); err != nil {
			log.Warn("delete proforma record", sl.Err(err))
		}
	}
	return nil
}

// reuseProforma returns the remembered payment with fresh links, downloading again any
// PDF that is no longer on disk.
func (c *Core) reuseProforma(ctx context.Context, stored *entity.Payment) (*entity.Payment, error) {
	if stored == nil {
		return nil, fmt.Errorf("record carries no payment")
	}
	payment := *stored

	fileName, link, err := c.downloadInvoice(ctx, c.presentFile(payment.InvoiceFile), payment.Id)
	if err != nil {
		return nil, err
	}
	payment.InvoiceFile, payment.Link = fileName, link

	if len(stored.Parts) == 0 {
		return &payment, nil
	}
	payment.Parts = make([]*entity.Payment, 0, len(stored.Parts))
	for _, p := range stored.Parts {
		if p == nil {
			continue
		}
		part := *p
		if part.Id == payment.Id {
			part.InvoiceFile, part.Link = payment.InvoiceFile, payment.Link
		} else {
			part.InvoiceFile, part.Link, err = c.downloadInvoice(ctx, c.presentFile(part.InvoiceFile), part.Id)
			if err != nil {
				return nil, err
			}
		}
		payment.Parts = append(payment.Parts, &part)
	}
	return &payment, nil
}

// presentFile returns name when that file is still on disk, "" otherwise so the caller
// downloads it again.
func (c *Core) presentFile(name string) string {
	if name == "" {
		return ""
	}
	if _, err := os.Stat(filepath.Join(c.filePath, name)); err != nil {
		return ""
	}
	return name
}

// proformaFiles lists the distinct local file names held by a payment and its parts.
func proformaFiles(p *entity.Payment) []string {
	if p == nil {
		return nil
	}
	var names []string
	for _, doc := range append([]*entity.Payment{p}, p.Parts...) {
		if doc != nil && doc.InvoiceFile != "" && !slices.Contains(names, doc.InvoiceFile) {
			names = append(names, doc.InvoiceFile)
		}
	}
	return names
}

// containsAll reports whether every id in want is among have; an empty want never matches.
func containsAll(have, want []string) bool {
	if len(want) == 0 {
		return false
	}
	for _, id := range want {
		if !slices.Contains(have, id) {
			return false
		}
	}
	return true
}

// without returns the ids of all that are not in drop, in order.
func without(all, drop []string) []string {
	var out []string
	for _, id := range all {
		if !slices.Contains(drop, id) {
			out = append(out, id)
		}
	}
	return out
}
