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
// The issued set is known only from this service's own record: wFirma's invoices/find
// does not match proformas by id_external, so the record is the source of truth. When
// the data is unchanged and every recorded document still exists, the set is reused;
// otherwise the recorded documents are deleted and a new set is issued, so an order never
// carries more than one set.
//
// A large order is split into several documents, and issuing them can outlast the
// caller's HTTP timeout. The work is therefore detached from the request and serialized
// per order: a caller that gave up and retries waits for the running issuance and then
// receives its result, instead of issuing a second set.
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

	record, err := c.proformaRecord(ref)
	if err != nil {
		// Without the record the issued set is unknown — issuing now would leave a
		// second set beside it.
		return nil, fmt.Errorf("read proforma record: %w", err)
	}

	if record != nil && record.Fingerprint == fingerprint {
		present, err := c.proformasPresent(ctx, record.DocumentIds())
		if err != nil {
			return nil, fmt.Errorf("check issued proformas: %w", err)
		}
		if present {
			// The documents are valid and may already be with the client; a failed PDF
			// download is a reason to retry, never to replace them under new numbers.
			payment, err := c.reuseProforma(ctx, record.Payment)
			if err != nil {
				return nil, fmt.Errorf("reuse issued proforma: %w", err)
			}
			log.With(slog.Any("proforma_ids", record.DocumentIds())).Info("proforma unchanged, reusing issued documents")
			return payment, nil
		}
		log.With(slog.Any("proforma_ids", record.DocumentIds())).Info("issued proforma missing in wFirma, issuing anew")
	}

	if err := c.discardProformas(ctx, ref, record, log); err != nil {
		return nil, err
	}

	payment, err := c.inv.RegisterProforma(ctx, params)
	if err != nil {
		// A set that failed part-way comes back with the parts already registered;
		// nothing records them, so remove them now or they stay behind as strays.
		c.rollbackProforma(ctx, payment, log)
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

// proformaRecord returns the remembered proforma set for ref, nil when there is none.
func (c *Core) proformaRecord(ref string) (*entity.ProformaRecord, error) {
	if c.proformaDb == nil || ref == "" {
		return nil, nil
	}
	return c.proformaDb.GetProformaRecord(ref)
}

// proformasPresent reports whether every document of a recorded set still exists in
// wFirma — one may have been deleted there by hand.
func (c *Core) proformasPresent(ctx context.Context, ids []string) (bool, error) {
	if len(ids) == 0 {
		return false, nil
	}
	for _, id := range ids {
		exists, err := c.inv.InvoiceExists(ctx, id)
		if err != nil {
			return false, err
		}
		if !exists {
			return false, nil
		}
	}
	return true, nil
}

// discardProformas deletes the recorded proforma set in wFirma, then its local files and
// the record itself. A failed wFirma delete aborts: issuing a new set beside a document
// that could not be removed is the duplicate this prevents. Documents already gone are
// skipped by DeleteProforma.
func (c *Core) discardProformas(ctx context.Context, ref string, record *entity.ProformaRecord, log *slog.Logger) error {
	if record == nil {
		return nil
	}
	ids := record.DocumentIds()
	for _, id := range ids {
		if err := c.inv.DeleteProforma(ctx, id); err != nil {
			return fmt.Errorf("delete stale proforma %s: %w", id, err)
		}
	}
	if len(ids) > 0 {
		log.With(slog.Any("proforma_ids", ids)).Info("stale proformas deleted")
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

// rollbackProforma deletes the parts of a proforma set that failed part-way. It is best
// effort: a part it cannot delete is logged for removal by hand.
func (c *Core) rollbackProforma(ctx context.Context, partial *entity.Payment, log *slog.Logger) {
	if partial == nil {
		return
	}
	for _, id := range (&entity.ProformaRecord{Payment: partial}).DocumentIds() {
		if err := c.inv.DeleteProforma(ctx, id); err != nil {
			log.With(
				slog.String("proforma_id", id),
				slog.String("tg_topic", entity.TopicError),
			).Error("delete part of failed proforma set, remove it in wFirma by hand", sl.Err(err))
		}
	}
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
