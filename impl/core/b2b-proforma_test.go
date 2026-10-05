package core

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"wfsync/entity"
	"wfsync/lib/keylock"
)

// proformaInvoiceService is a stub wFirma holding proformas in memory. Each registration
// issues parts documents, mirroring how a large order is split.
type proformaInvoiceService struct {
	InvoiceService
	dir         string
	parts       int
	nextId      int
	registered  []string
	registers   int
	downloads   int
	findErr     error
	deleteErr   error
	downloadErr error
}

func (s *proformaInvoiceService) ExpectedB2BVATRate(_ string, _ bool) int { return 0 }

func (s *proformaInvoiceService) FindProformaIds(_ context.Context, _ string) ([]string, error) {
	if s.findErr != nil {
		return nil, s.findErr
	}
	return slices.Clone(s.registered), nil
}

func (s *proformaInvoiceService) DeleteProforma(_ context.Context, id string) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	s.registered = slices.DeleteFunc(s.registered, func(r string) bool { return r == id })
	return nil
}

func (s *proformaInvoiceService) RegisterProforma(_ context.Context, params *entity.CheckoutParams) (*entity.Payment, error) {
	s.registers++
	var parts []*entity.Payment
	for i := 0; i < s.parts; i++ {
		s.nextId++
		id := strconv.Itoa(s.nextId)
		s.registered = append(s.registered, id)
		parts = append(parts, &entity.Payment{Id: id, OrderId: params.OrderId})
	}
	head := *parts[0]
	if len(parts) > 1 {
		head.Parts = parts
	}
	return &head, nil
}

func (s *proformaInvoiceService) DownloadInvoice(_ context.Context, id string) (string, *entity.FileMeta, error) {
	if s.downloadErr != nil {
		return "", nil, s.downloadErr
	}
	s.downloads++
	name := "proforma-" + id + ".pdf"
	if err := os.WriteFile(filepath.Join(s.dir, name), []byte("pdf"), 0o644); err != nil {
		return "", nil, err
	}
	return name, nil, nil
}

// memProformaDatabase is an in-memory ProformaDatabase.
type memProformaDatabase struct {
	records map[string]*entity.ProformaRecord
}

func (d *memProformaDatabase) GetProformaRecord(ref string) (*entity.ProformaRecord, error) {
	return d.records[ref], nil
}

func (d *memProformaDatabase) SaveProformaRecord(r *entity.ProformaRecord) error {
	d.records[r.ExternalId] = r
	return nil
}

func (d *memProformaDatabase) DeleteProformaRecord(ref string) error {
	delete(d.records, ref)
	return nil
}

func proformaTestCore(t *testing.T, parts int) (*Core, *proformaInvoiceService, *memProformaDatabase) {
	t.Helper()
	dir := t.TempDir()
	inv := &proformaInvoiceService{dir: dir, parts: parts}
	db := &memProformaDatabase{records: map[string]*entity.ProformaRecord{}}
	c := &Core{
		inv:           inv,
		proformaDb:    db,
		proformaLocks: keylock.New(),
		filePath:      dir,
		fileUrl:       "https://files.test/",
		log:           slog.Default(),
	}
	return c, inv, db
}

func testB2BOrder() *entity.B2BOrder {
	return &entity.B2BOrder{
		OrderUID:      "uid-600",
		OrderNumber:   "PL-600",
		ClientName:    "Nail Look",
		ClientEmail:   "client@example.com",
		ClientCountry: "PL",
		ClientAddress: "Street 1",
		Total:         30,
		CurrencyCode:  "EUR",
		Items: []*entity.B2BItem{
			{ProductSKU: "A", ProductName: "A", Quantity: 1, Price: 10},
			{ProductSKU: "B", ProductName: "B", Quantity: 2, Price: 10},
		},
	}
}

func paymentIds(p *entity.Payment) []string {
	if len(p.Parts) == 0 {
		return []string{p.Id}
	}
	var ids []string
	for _, part := range p.Parts {
		ids = append(ids, part.Id)
	}
	return ids
}

func TestB2BCreateProformaReusesUnchanged(t *testing.T) {
	c, inv, _ := proformaTestCore(t, 2)
	ctx := context.Background()

	first, err := c.B2BCreateProforma(ctx, testB2BOrder())
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := c.B2BCreateProforma(ctx, testB2BOrder())
	if err != nil {
		t.Fatalf("second: %v", err)
	}

	if inv.registers != 1 {
		t.Errorf("registrations = %d, want 1", inv.registers)
	}
	if inv.downloads != 2 {
		t.Errorf("downloads = %d, want 2 (one per part, none on reuse)", inv.downloads)
	}
	if !slices.Equal(paymentIds(first), paymentIds(second)) {
		t.Errorf("reused ids = %v, want %v", paymentIds(second), paymentIds(first))
	}
	for _, part := range second.Parts {
		if part.Link == "" || part.InvoiceFile == "" {
			t.Errorf("part %s returned without file: %+v", part.Id, part)
		}
	}
}

func TestB2BCreateProformaReissuesOnChange(t *testing.T) {
	changes := map[string]func(o *entity.B2BOrder){
		"quantity": func(o *entity.B2BOrder) { o.Items[1].Quantity = 3; o.Total = 40 },
		"total":    func(o *entity.B2BOrder) { o.Items[0].Price = 11; o.Total = 31 },
		"client":   func(o *entity.B2BOrder) { o.ClientName = "Other" },
		"address":  func(o *entity.B2BOrder) { o.BillingAddress = "Billing 2" },
		"item":     func(o *entity.B2BOrder) { o.Items[0].ProductSKU = "C" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			c, inv, db := proformaTestCore(t, 2)
			ctx := context.Background()

			first, err := c.B2BCreateProforma(ctx, testB2BOrder())
			if err != nil {
				t.Fatalf("first: %v", err)
			}
			order := testB2BOrder()
			change(order)
			second, err := c.B2BCreateProforma(ctx, order)
			if err != nil {
				t.Fatalf("second: %v", err)
			}

			if inv.registers != 2 {
				t.Errorf("registrations = %d, want 2", inv.registers)
			}
			if !slices.Equal(inv.registered, paymentIds(second)) {
				t.Errorf("wFirma holds %v, want only the new set %v", inv.registered, paymentIds(second))
			}
			for _, part := range first.Parts {
				if _, err := os.Stat(filepath.Join(c.filePath, part.InvoiceFile)); !os.IsNotExist(err) {
					t.Errorf("stale file %s left on disk", part.InvoiceFile)
				}
			}
			if rec := db.records["uid-600"]; rec == nil || !slices.Equal(rec.DocumentIds(), paymentIds(second)) {
				t.Errorf("record not updated to the new set: %+v", rec)
			}
		})
	}
}

// Proformas registered without a matching record — a run that died before saving it, or
// one issued before records existed — are replaced rather than left beside the new set.
func TestB2BCreateProformaReplacesUnrecordedDocuments(t *testing.T) {
	c, inv, _ := proformaTestCore(t, 1)
	inv.registered = []string{"90", "91"}

	payment, err := c.B2BCreateProforma(context.Background(), testB2BOrder())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(inv.registered, []string{payment.Id}) {
		t.Errorf("wFirma holds %v, want only %s", inv.registered, payment.Id)
	}
}

// A document deleted by hand in wFirma no longer matches the record, so the set is issued again.
func TestB2BCreateProformaReissuesWhenDocumentGone(t *testing.T) {
	c, inv, _ := proformaTestCore(t, 2)
	ctx := context.Background()

	if _, err := c.B2BCreateProforma(ctx, testB2BOrder()); err != nil {
		t.Fatal(err)
	}
	inv.registered = inv.registered[:1]

	if _, err := c.B2BCreateProforma(ctx, testB2BOrder()); err != nil {
		t.Fatal(err)
	}
	if inv.registers != 2 {
		t.Errorf("registrations = %d, want 2", inv.registers)
	}
	if len(inv.registered) != 2 {
		t.Errorf("wFirma holds %v, want one fresh set of 2", inv.registered)
	}
}

// A failed re-download of a missing PDF fails the request but leaves the issued
// documents alone: they are valid and may already be with the client.
func TestB2BCreateProformaKeepsDocumentsWhenDownloadFails(t *testing.T) {
	c, inv, _ := proformaTestCore(t, 2)
	ctx := context.Background()

	first, err := c.B2BCreateProforma(ctx, testB2BOrder())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(c.filePath, first.Parts[1].InvoiceFile)); err != nil {
		t.Fatal(err)
	}
	inv.downloadErr = errors.New("timeout")

	if _, err := c.B2BCreateProforma(ctx, testB2BOrder()); err == nil {
		t.Fatal("expected an error")
	}
	if inv.registers != 1 || !slices.Equal(inv.registered, paymentIds(first)) {
		t.Errorf("documents changed: registrations %d, wFirma holds %v", inv.registers, inv.registered)
	}
}

// A PDF missing from disk is downloaded again; the documents themselves are reused.
func TestB2BCreateProformaRedownloadsMissingFile(t *testing.T) {
	c, inv, _ := proformaTestCore(t, 2)
	ctx := context.Background()

	first, err := c.B2BCreateProforma(ctx, testB2BOrder())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(c.filePath, first.Parts[1].InvoiceFile)); err != nil {
		t.Fatal(err)
	}

	if _, err := c.B2BCreateProforma(ctx, testB2BOrder()); err != nil {
		t.Fatal(err)
	}
	if inv.registers != 1 {
		t.Errorf("registrations = %d, want 1", inv.registers)
	}
	if inv.downloads != 3 {
		t.Errorf("downloads = %d, want 3 (2 initial + 1 missing)", inv.downloads)
	}
}

func TestB2BCreateProformaAbortsOnUnknownState(t *testing.T) {
	tests := map[string]func(inv *proformaInvoiceService){
		"lookup fails": func(inv *proformaInvoiceService) { inv.findErr = errors.New("timeout") },
		"delete fails": func(inv *proformaInvoiceService) {
			inv.registered = []string{"90"}
			inv.deleteErr = errors.New("timeout")
		},
	}
	for name, setup := range tests {
		t.Run(name, func(t *testing.T) {
			c, inv, _ := proformaTestCore(t, 1)
			setup(inv)

			if _, err := c.B2BCreateProforma(context.Background(), testB2BOrder()); err == nil {
				t.Fatal("expected an error")
			}
			if inv.registers != 0 {
				t.Errorf("registrations = %d, want 0", inv.registers)
			}
		})
	}
}
