package core

import (
	"context"
	"log/slog"
	"testing"
	"time"
	"wfsync/entity"
)

// listInvoiceService is a stub InvoiceService returning a fixed set of wFirma documents.
type listInvoiceService struct {
	InvoiceService
	invoices []*entity.LocalInvoice
	err      error
}

func (s *listInvoiceService) FindInvoices(_ context.Context, _, _ string) ([]*entity.LocalInvoice, error) {
	return s.invoices, s.err
}

// listDatabase is a stub PaymentDatabase serving checkout params from memory.
type listDatabase struct {
	PaymentDatabase
	inRange []*entity.CheckoutParams
	byRef   []*entity.CheckoutParams
}

func (d *listDatabase) GetCheckoutParamsByDateRange(_, _ string) ([]*entity.CheckoutParams, error) {
	return d.inRange, nil
}

func (d *listDatabase) GetCheckoutParamsByRefs(refs []string) ([]*entity.CheckoutParams, error) {
	var out []*entity.CheckoutParams
	for _, cp := range d.byRef {
		for _, ref := range refs {
			if cp.OrderId == ref || cp.ExternalId == ref {
				out = append(out, cp)
				break
			}
		}
	}
	return out, nil
}

func testCore(inv InvoiceService, db PaymentDatabase) *Core {
	return &Core{inv: inv, db: db, log: slog.Default()}
}

func itemByOrderId(items []*entity.InvoiceListItem, orderId string) *entity.InvoiceListItem {
	for _, i := range items {
		if i.OrderId == orderId {
			return i
		}
	}
	return nil
}

// A B2B order is invoiced under its UID (id_external) while its OrderId stays the human
// order number: the two must collapse into one row, not two.
func TestInvoiceListMatchesB2BOrderByExternalId(t *testing.T) {
	created := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	c := testCore(
		&listInvoiceService{invoices: []*entity.LocalInvoice{
			{Id: "9", Number: "FV 1/2026", Type: "normal", Date: "2026-07-11", Currency: "PLN",
				Total: 12.34, IdExternal: "uid-abc"},
		}},
		&listDatabase{inRange: []*entity.CheckoutParams{
			{OrderId: "1001", ExternalId: "uid-abc", Source: entity.SourceB2B,
				Currency: "PLN", Total: 1234, Created: created},
		}},
	)

	result, err := c.InvoiceList(context.Background(), "2026-07-01", "2026-07-31")
	if err != nil {
		t.Fatalf("InvoiceList: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("expected 1 merged item, got %d", len(result.Items))
	}
	item := result.Items[0]
	if item.OrderId != "1001" || item.ExternalId != "uid-abc" {
		t.Errorf("identity not merged: order_id=%q external_id=%q", item.OrderId, item.ExternalId)
	}
	if !item.Invoiced || item.DocumentType != entity.DocumentInvoice || item.InvoiceNumber != "FV 1/2026" {
		t.Errorf("invoice not attached: %+v", item)
	}
	if !item.IsB2B {
		t.Error("expected B2B order")
	}
	if item.IsStripe {
		t.Error("order without a checkout session must not be reported as Stripe")
	}
	if result.Summary.Invoiced != 1 || result.Summary.NotInvoiced != 0 {
		t.Errorf("unexpected summary: %+v", result.Summary)
	}
}

// A KSeF draft is a real registration with no number; it must be reported as a draft
// rather than as an uninvoiced order.
func TestInvoiceListReportsDrafts(t *testing.T) {
	c := testCore(
		&listInvoiceService{invoices: []*entity.LocalInvoice{
			{Id: "7", Type: "normal_draft", Date: "2026-07-05", Currency: "PLN", Total: 100, IdExternal: "2002"},
		}},
		&listDatabase{inRange: []*entity.CheckoutParams{
			{OrderId: "2002", Source: entity.SourceApi, Currency: "PLN", Total: 10000,
				Created: time.Date(2026, 7, 5, 9, 0, 0, 0, time.UTC)},
		}},
	)

	result, err := c.InvoiceList(context.Background(), "2026-07-01", "2026-07-31")
	if err != nil {
		t.Fatalf("InvoiceList: %v", err)
	}
	item := itemByOrderId(result.Items, "2002")
	if item == nil {
		t.Fatal("order 2002 missing from list")
	}
	if !item.Invoiced || item.DocumentType != entity.DocumentDraft {
		t.Errorf("draft not reported: invoiced=%v type=%q", item.Invoiced, item.DocumentType)
	}
	if result.Summary.Drafts != 1 || result.Summary.Invoiced != 0 {
		t.Errorf("unexpected summary: %+v", result.Summary)
	}
}

// Several faktura parts share one id_external when an order is split; they belong to a
// single row whose amounts are summed.
func TestInvoiceListAggregatesSplitInvoiceParts(t *testing.T) {
	c := testCore(
		&listInvoiceService{invoices: []*entity.LocalInvoice{
			{Id: "1", Number: "FV 2/2026", Type: "normal", Date: "2026-07-08", Currency: "EUR", Total: 20, IdExternal: "3003"},
			{Id: "2", Number: "FV 1/2026", Type: "normal", Date: "2026-07-07", Currency: "EUR", Total: 30, IdExternal: "3003"},
		}},
		&listDatabase{},
	)

	result, err := c.InvoiceList(context.Background(), "2026-07-01", "2026-07-31")
	if err != nil {
		t.Fatalf("InvoiceList: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("expected split parts to collapse into 1 item, got %d", len(result.Items))
	}
	item := result.Items[0]
	if item.InvoiceParts != 2 {
		t.Errorf("expected 2 parts, got %d", item.InvoiceParts)
	}
	if item.InvoiceNumber != "FV 1/2026, FV 2/2026" {
		t.Errorf("unexpected numbers: %q", item.InvoiceNumber)
	}
	if item.TotalEUR != 5000 {
		t.Errorf("expected summed total 5000, got %d", item.TotalEUR)
	}
	if item.InvoiceDate != "2026-07-07" {
		t.Errorf("expected earliest part date, got %q", item.InvoiceDate)
	}
}

// An order that was never invoiced exists only in checkout_params — it is the case the
// report exists to surface, so it must appear with invoiced=false.
func TestInvoiceListIncludesUninvoicedOrders(t *testing.T) {
	c := testCore(
		&listInvoiceService{},
		&listDatabase{inRange: []*entity.CheckoutParams{
			{OrderId: "4004", Source: entity.SourceStripe, SessionId: "cs_test_1",
				Currency: "PLN", Total: 5000, Created: time.Date(2026, 7, 3, 8, 0, 0, 0, time.UTC),
				ClientDetails: &entity.ClientDetails{Name: "Jan Kowalski"}},
		}},
	)

	result, err := c.InvoiceList(context.Background(), "2026-07-01", "2026-07-31")
	if err != nil {
		t.Fatalf("InvoiceList: %v", err)
	}
	item := itemByOrderId(result.Items, "4004")
	if item == nil {
		t.Fatal("uninvoiced order missing from list")
	}
	if item.Invoiced || item.DocumentType != entity.DocumentNone {
		t.Errorf("expected uninvoiced order, got %+v", item)
	}
	if !item.IsStripe {
		t.Error("order with a checkout session must be reported as Stripe")
	}
	if item.Date != "2026-07-03" || item.ContractorName != "Jan Kowalski" {
		t.Errorf("checkout params data not applied: %+v", item)
	}
	if result.Summary.NotInvoiced != 1 {
		t.Errorf("unexpected summary: %+v", result.Summary)
	}
}

// A failing source must be reported, so that an empty list is never mistaken for
// "nothing was invoiced".
func TestInvoiceListReportsSourceFailure(t *testing.T) {
	c := testCore(&listInvoiceService{err: context.DeadlineExceeded}, &listDatabase{})

	result, err := c.InvoiceList(context.Background(), "2026-07-01", "2026-07-31")
	if err != nil {
		t.Fatalf("InvoiceList: %v", err)
	}
	var wf *entity.InvoiceListSource
	for _, s := range result.Sources {
		if s.Name == "wfirma" {
			wf = s
		}
	}
	if wf == nil {
		t.Fatal("wfirma source not reported")
	}
	if wf.Ok || wf.Error == "" {
		t.Errorf("expected failed wfirma source, got %+v", wf)
	}
}

// An invoice with no id_external still names its order in the description this service
// writes. Without the fallback it lands in the orphan bucket and its order is reported as
// never invoiced — the very false negative the report exists to rule out.
func TestInvoiceListMatchesByDescriptionWhenExternalIdMissing(t *testing.T) {
	created := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	c := testCore(
		&listInvoiceService{invoices: []*entity.LocalInvoice{
			{Id: "11", Number: "FV 1830/2026", Type: "normal", Date: "2026-07-30", Currency: "PLN",
				Total: 1161.05, Description: "Numer zamówienia: 17098"},
			{Id: "12", Number: "FV 1832/2026", Type: "normal", Date: "2026-07-30", Currency: "PLN",
				Total: 475.34, Description: "Numer zamówienia: 17099 (część 1/2)"},
		}},
		&listDatabase{inRange: []*entity.CheckoutParams{
			{OrderId: "17098", Source: entity.SourceOpenCart, Currency: "PLN", Total: 116105, Created: created},
			{OrderId: "17099", Source: entity.SourceOpenCart, Currency: "PLN", Total: 47534, Created: created},
		}},
	)

	result, err := c.InvoiceList(context.Background(), "2026-07-01", "2026-07-31")
	if err != nil {
		t.Fatalf("InvoiceList: %v", err)
	}
	for _, orderId := range []string{"17098", "17099"} {
		item := itemByOrderId(result.Items, orderId)
		if item == nil {
			t.Fatalf("order %s missing from list", orderId)
		}
		if !item.Invoiced {
			t.Errorf("order %s reported as not invoiced: %+v", orderId, item)
		}
	}
	if result.Summary.Invoiced != 2 || result.Summary.NotInvoiced != 0 {
		t.Errorf("unexpected summary: %+v", result.Summary)
	}
}

// The stamped id_external is authoritative: a description naming a different order must
// never move an invoice off the order wFirma itself records it under.
func TestInvoiceListPrefersExternalIdOverDescription(t *testing.T) {
	c := testCore(
		&listInvoiceService{invoices: []*entity.LocalInvoice{
			{Id: "13", Number: "FV 9/2026", Type: "normal", Date: "2026-07-30", Currency: "PLN",
				Total: 10, IdExternal: "uid-xyz", Description: "Numer zamówienia: 17098"},
		}},
		&listDatabase{inRange: []*entity.CheckoutParams{
			{OrderId: "17098", ExternalId: "uid-xyz", Source: entity.SourceB2B, Currency: "PLN",
				Total: 1000, Created: time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)},
		}},
	)

	result, err := c.InvoiceList(context.Background(), "2026-07-01", "2026-07-31")
	if err != nil {
		t.Fatalf("InvoiceList: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("expected one row, got %d: %+v", len(result.Items), result.Items)
	}
	if !result.Items[0].Invoiced || result.Items[0].OrderId != "17098" {
		t.Errorf("unexpected row: %+v", result.Items[0])
	}
}

func TestOrderRefFromDescription(t *testing.T) {
	cases := map[string]string{
		"Numer zamówienia: 17098":             "17098",
		"Numer zamówienia: 17099 (część 1/2)": "17099",
		"Numer zamówienia: ORD-73917800005":   "ORD-73917800005",
		"Zamówienie 17098":                    "",
		"":                                    "",
	}
	for desc, want := range cases {
		if got := orderRefFromDescription(desc); got != want {
			t.Errorf("orderRefFromDescription(%q) = %q, want %q", desc, got, want)
		}
	}
}
