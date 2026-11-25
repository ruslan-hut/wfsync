package core

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"wfsync/entity"
)

func testSiteOrder() *entity.CheckoutParams {
	return &entity.CheckoutParams{
		OrderId:  "18400",
		Currency: "PLN",
		Total:    3000,
		ClientDetails: &entity.ClientDetails{
			Name:    "Jan Kowalski",
			Email:   "jan@example.com",
			Country: "PL",
		},
		LineItems: []*entity.LineItem{
			{Sku: "A", Name: "A", Qty: 1, Price: 1000},
			{Sku: "B", Name: "B", Qty: 2, Price: 1000},
		},
	}
}

// siteOrderAfter returns the order as OpenCart serves it after payment was saved on it.
func siteOrderAfter(payment *entity.Payment) *entity.CheckoutParams {
	order := testSiteOrder()
	if payment != nil {
		order.ProformaId, order.ProformaFile = payment.Id, payment.InvoiceFile
	}
	return order
}

func TestOrderProformaReusesUnchanged(t *testing.T) {
	for _, parts := range []int{1, 2} {
		c, inv, _ := proformaTestCore(t, parts)
		ctx := context.Background()

		first, err := c.orderProforma(ctx, testSiteOrder())
		if err != nil {
			t.Fatalf("parts %d, first: %v", parts, err)
		}
		if first.Reused {
			t.Errorf("parts %d: new proforma reported as reused", parts)
		}
		second, err := c.orderProforma(ctx, siteOrderAfter(first))
		if err != nil {
			t.Fatalf("parts %d, second: %v", parts, err)
		}

		if inv.registers != 1 {
			t.Errorf("parts %d: registrations = %d, want 1", parts, inv.registers)
		}
		if !second.Reused {
			t.Errorf("parts %d: repeat not reported as reused", parts)
		}
		if second.Id != first.Id || second.InvoiceFile != first.InvoiceFile {
			t.Errorf("parts %d: reused %s/%s, want %s/%s", parts, second.Id, second.InvoiceFile, first.Id, first.InvoiceFile)
		}
		if !slices.Equal(paymentIds(second), paymentIds(first)) {
			t.Errorf("parts %d: reused ids %v, want %v", parts, paymentIds(second), paymentIds(first))
		}
	}
}

func TestOrderProformaReissuesOnChange(t *testing.T) {
	c, inv, db := proformaTestCore(t, 1)
	ctx := context.Background()

	first, err := c.orderProforma(ctx, testSiteOrder())
	if err != nil {
		t.Fatal(err)
	}
	order := siteOrderAfter(first)
	order.LineItems[1].Qty = 3
	order.Total = 4000

	second, err := c.orderProforma(ctx, order)
	if err != nil {
		t.Fatal(err)
	}
	if second.Reused || second.Id == first.Id {
		t.Errorf("changed order got the old proforma: %+v", second)
	}
	if !slices.Equal(inv.registered, []string{second.Id}) {
		t.Errorf("wFirma holds %v, want only %s", inv.registered, second.Id)
	}
	if _, err := os.Stat(filepath.Join(c.filePath, first.InvoiceFile)); !os.IsNotExist(err) {
		t.Errorf("stale file %s left on disk", first.InvoiceFile)
	}
	if rec := db.records["18400"]; rec == nil || rec.Payment.Id != second.Id {
		t.Errorf("record not updated: %+v", rec)
	}
}

// A proforma on the order without a record (issued by the status job or before records
// were kept) cannot be compared, so it is replaced as before.
func TestOrderProformaReplacesUnrecorded(t *testing.T) {
	c, inv, _ := proformaTestCore(t, 1)
	inv.registered = []string{"90"}
	order := testSiteOrder()
	order.ProformaId = "90"

	payment, err := c.orderProforma(context.Background(), order)
	if err != nil {
		t.Fatal(err)
	}
	if payment.Reused {
		t.Error("unrecorded proforma reported as reused")
	}
	if !slices.Equal(inv.registered, []string{payment.Id}) {
		t.Errorf("wFirma holds %v, want only %s", inv.registered, payment.Id)
	}
}

// The status job re-issued the proforma after the site got its own: the order now points
// at a document the record does not know, so neither is reused and both are removed.
func TestOrderProformaReissuesWhenOrderPointsElsewhere(t *testing.T) {
	c, inv, _ := proformaTestCore(t, 1)
	ctx := context.Background()

	if _, err := c.orderProforma(ctx, testSiteOrder()); err != nil {
		t.Fatal(err)
	}
	inv.registered = append(inv.registered, "95")
	order := testSiteOrder()
	order.ProformaId = "95"

	payment, err := c.orderProforma(ctx, order)
	if err != nil {
		t.Fatal(err)
	}
	if payment.Reused {
		t.Error("proforma reused although the order points at another one")
	}
	if !slices.Equal(inv.registered, []string{payment.Id}) {
		t.Errorf("wFirma holds %v, want only %s", inv.registered, payment.Id)
	}
}

// Without the record store every call issues anew — the behaviour before records existed.
func TestOrderProformaWithoutRecordStore(t *testing.T) {
	c, inv, _ := proformaTestCore(t, 1)
	c.proformaDb = nil
	ctx := context.Background()

	first, err := c.orderProforma(ctx, testSiteOrder())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.orderProforma(ctx, siteOrderAfter(first)); err != nil {
		t.Fatal(err)
	}
	if inv.registers != 2 || len(inv.registered) != 1 {
		t.Errorf("registrations %d, wFirma holds %v; want 2 and a single document", inv.registers, inv.registered)
	}
}

// The customer group decides VAT treatment, so changing it alone re-issues.
func TestOrderProformaReissuesOnCustomerGroup(t *testing.T) {
	c, inv, _ := proformaTestCore(t, 1)
	ctx := context.Background()

	first, err := c.orderProforma(ctx, testSiteOrder())
	if err != nil {
		t.Fatal(err)
	}
	order := siteOrderAfter(first)
	order.CustomerGroup = 2

	second, err := c.orderProforma(ctx, order)
	if err != nil {
		t.Fatal(err)
	}
	if second.Reused || inv.registers != 2 {
		t.Errorf("customer group change reused the proforma: registrations %d", inv.registers)
	}
}
