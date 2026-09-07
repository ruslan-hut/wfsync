package wfirma

import (
	"context"
	"testing"
	"wfsync/entity"
)

// stubVIES returns a fixed verdict for every number, so a test can pin the exact
// branch of the VIES switch that invoice() takes.
type stubVIES struct {
	result entity.VIESResult
	calls  int
}

func (s *stubVIES) ValidateTaxId(_, _ string) entity.VIESResult {
	s.calls++
	return s.result
}

// frB2CParams builds the shape that motivated the promotion: an EU company buying
// through a retail customer group, VAT number supplied. Without promotion this is an
// OSS sale at the French rate; with it, a WDT intra-community delivery.
func frB2CParams(orderId string) *entity.CheckoutParams {
	return &entity.CheckoutParams{
		OrderId:       orderId,
		Currency:      "PLN",
		Total:         735243,
		CustomerGroup: 14,
		LineItems:     []*entity.LineItem{{Name: "Item", Qty: 1, Price: 735243}},
		ClientDetails: &entity.ClientDetails{
			Name: "Buyer", Email: "buyer@example.com", Country: "France, Metropolitan",
			City: "Paris", ZipCode: "75001", TaxId: "FR24922403365",
		},
	}
}

// vatOfFirstLine returns the vat field wFirma received on the invoice's first line.
func vatOfFirstLine(t *testing.T, f *fakeWfirma) string {
	t.Helper()
	if f.addCount() == 0 {
		t.Fatal("no invoice was created")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	contents := f.added[0].Contents
	if len(contents) == 0 || contents[0].Content == nil {
		t.Fatal("invoice has no content lines")
	}
	return contents[0].Content.Vat
}

// A VIES-confirmed VAT number outranks the B2C customer group: the order is invoiced
// as an intra-community delivery, not under OSS at the destination rate.
func TestVIESValidPromotesB2CGroupToB2B(t *testing.T) {
	c, fake := newGuardTestClient(t)
	vies := &stubVIES{result: entity.VIESValid}
	c.SetVIESProvider(vies)

	params := frB2CParams("17462")
	if _, err := c.RegisterInvoice(context.Background(), params); err != nil {
		t.Fatalf("RegisterInvoice: %v", err)
	}

	if vies.calls == 0 {
		t.Fatal("VIES was never consulted")
	}
	if got := vatOfFirstLine(t, fake); got != vatWDT {
		t.Errorf("line vat = %q, want %q", got, vatWDT)
	}
	if params.CustomerGroup != -1 {
		t.Errorf("customer group = %d, want -1 (B2B)", params.CustomerGroup)
	}
}

// An unverified number must never zero-rate an invoice: both a definitive "invalid"
// and an inconclusive check (VIES down) leave the order B2C at the French OSS rate.
func TestVIESNonValidLeavesOrderB2C(t *testing.T) {
	for name, result := range map[string]entity.VIESResult{
		"invalid":      entity.VIESInvalid,
		"inconclusive": entity.VIESInconclusive,
	} {
		t.Run(name, func(t *testing.T) {
			c, fake := newGuardTestClient(t)
			c.SetVIESProvider(&stubVIES{result: result})

			params := frB2CParams("17462")
			if _, err := c.RegisterInvoice(context.Background(), params); err != nil {
				t.Fatalf("RegisterInvoice: %v", err)
			}

			if got := vatOfFirstLine(t, fake); got == vatWDT {
				t.Errorf("line vat = %q, want the French destination rate, not WDT", got)
			}
			if params.CustomerGroup != 14 {
				t.Errorf("customer group = %d, want 14 (unchanged)", params.CustomerGroup)
			}
		})
	}
}

// With no VIES provider configured the promotion cannot fire — the group stays the
// sole authority, which is the behaviour every deployment without VIES relies on.
func TestNoVIESProviderLeavesOrderB2C(t *testing.T) {
	c, fake := newGuardTestClient(t)

	params := frB2CParams("17462")
	if _, err := c.RegisterInvoice(context.Background(), params); err != nil {
		t.Fatalf("RegisterInvoice: %v", err)
	}

	if got := vatOfFirstLine(t, fake); got == vatWDT {
		t.Errorf("line vat = %q, want the French destination rate, not WDT", got)
	}
	if params.CustomerGroup != 14 {
		t.Errorf("customer group = %d, want 14 (unchanged)", params.CustomerGroup)
	}
}

// A group that is already B2B must not be rewritten to the synthetic -1 flag: the
// OpenCart group id is worth keeping in logs and in the persisted order.
func TestVIESValidKeepsExistingB2BGroup(t *testing.T) {
	c, _ := newGuardTestClient(t)
	c.SetVIESProvider(&stubVIES{result: entity.VIESValid})

	params := frB2CParams("17462")
	params.CustomerGroup = 6
	if _, err := c.RegisterInvoice(context.Background(), params); err != nil {
		t.Fatalf("RegisterInvoice: %v", err)
	}

	if params.CustomerGroup != 6 {
		t.Errorf("customer group = %d, want 6 (unchanged)", params.CustomerGroup)
	}
}
