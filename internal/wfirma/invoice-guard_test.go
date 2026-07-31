package wfirma

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"wfsync/entity"
)

// fakeWfirma is a minimal stand-in for the wFirma API covering the calls invoice()
// makes: contractor lookup, VAT code lookup, and the invoices find/add pair the
// duplicate guard depends on. Added invoices are retained so that a later find sees
// them — that is what makes the guard observable end to end.
type fakeWfirma struct {
	mu       sync.Mutex
	added    []*Invoice // invoices accepted by add, in order
	nemo     int        // next invoice id
	failFrom int        // when > 0, reject the add call with this 1-based number and beyond
}

func (f *fakeWfirma) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/invoices/find"):
			f.writeFind(w, r)
		case strings.HasPrefix(r.URL.Path, "/invoices/add"):
			f.writeAdd(w, r)
		case strings.HasPrefix(r.URL.Path, "/vat_codes/find"):
			_, _ = io.WriteString(w, `{"status":{"code":"OK"},"vat_codes":{}}`)
		default:
			// Contractor find/edit — a stored contractor keeps the flow short.
			_, _ = io.WriteString(w, `{"status":{"code":"OK"},"contractors":{"0":{"contractor":{"id":"555","email":"buyer@example.com","name":"Buyer","city":"Warszawa","zip":"01-001","country":"PL"}}}}`)
		}
	})
}

// writeFind answers invoices/find for the id_external condition in the request body.
func (f *fakeWfirma) writeFind(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	ref := externalRefFromFindRequest(body)

	f.mu.Lock()
	defer f.mu.Unlock()

	entries := []string{}
	for _, inv := range f.added {
		if ref != "" && inv.IdExternal != ref {
			continue
		}
		entries = append(entries, fmt.Sprintf(
			`"%d":{"invoice":{"id":"%s","fullnumber":"%s","type":"%s","id_external":"%s","total":"%.2f"}}`,
			len(entries), inv.Id, inv.Number, inv.Type, inv.IdExternal, inv.Total))
	}
	// The real API returns pagination metadata as an extra map entry with no invoice.
	entries = append(entries, `"parameters":{"limit":"100","page":"1","total":"1"}`)
	_, _ = io.WriteString(w, fmt.Sprintf(`{"status":{"code":"OK"},"invoices":{%s}}`, strings.Join(entries, ",")))
}

func (f *fakeWfirma) writeAdd(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req struct {
		API struct {
			Invoices []struct {
				Invoice *Invoice `json:"invoice"`
			} `json:"invoices"`
		} `json:"api"`
	}
	if err := json.Unmarshal(body, &req); err != nil || len(req.API.Invoices) == 0 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failFrom > 0 && len(f.added)+1 >= f.failFrom {
		_, _ = io.WriteString(w, `{"status":{"code":"ERROR"},"invoices":{"0":{"invoice":{"errors":{"0":{"error":{"field":"total","message":"simulated failure"}}}}}}}`)
		return
	}
	f.nemo++
	inv := req.API.Invoices[0].Invoice
	inv.Id = fmt.Sprintf("%d", f.nemo)
	inv.Number = fmt.Sprintf("FV %d/2026", f.nemo)
	f.added = append(f.added, inv)

	_, _ = io.WriteString(w, fmt.Sprintf(
		`{"status":{"code":"OK"},"invoices":{"0":{"invoice":{"id":"%s","fullnumber":"%s","type":"%s"}}}}`,
		inv.Id, inv.Number, inv.Type))
}

// externalRefFromFindRequest pulls the id_external value out of a find request's
// conditions so the fake can filter like the real API does.
func externalRefFromFindRequest(body []byte) string {
	var req struct {
		API struct {
			Invoices struct {
				Parameters struct {
					Conditions struct {
						And []struct {
							Condition struct {
								Field string `json:"field"`
								Value string `json:"value"`
							} `json:"condition"`
						} `json:"and"`
					} `json:"conditions"`
				} `json:"parameters"`
			} `json:"invoices"`
		} `json:"api"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return ""
	}
	for _, c := range req.API.Invoices.Parameters.Conditions.And {
		if c.Condition.Field == "id_external" {
			return c.Condition.Value
		}
	}
	return ""
}

func (f *fakeWfirma) addCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.added)
}

func newGuardTestClient(t *testing.T) (*Client, *fakeWfirma) {
	t.Helper()
	fake := &fakeWfirma{}
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)

	return &Client{
		enabled:    true,
		hc:         srv.Client(),
		baseURL:    srv.URL,
		orderLocks: newOrderLocks(),
		log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, fake
}

// guardTestParams builds a PL B2C order of n identical items, one per line.
func guardTestParams(orderId string, n int) *entity.CheckoutParams {
	items := make([]*entity.LineItem, 0, n)
	for i := 0; i < n; i++ {
		items = append(items, &entity.LineItem{Name: fmt.Sprintf("Item %d", i), Qty: 1, Price: 10000})
	}
	return &entity.CheckoutParams{
		OrderId:   orderId,
		Currency:  "PLN",
		Total:     int64(n) * 10000,
		LineItems: items,
		ClientDetails: &entity.ClientDetails{
			Name: "Buyer", Email: "buyer@example.com", Country: "PL",
			City: "Warszawa", ZipCode: "01-001",
		},
	}
}

// The ordinary case: no faktura for the order yet, so one is created.
func TestRegisterInvoiceCreatesWhenNoneExists(t *testing.T) {
	c, fake := newGuardTestClient(t)

	payment, err := c.RegisterInvoice(context.Background(), guardTestParams("17098", 3))
	if err != nil {
		t.Fatalf("RegisterInvoice: %v", err)
	}
	if fake.addCount() != 1 {
		t.Fatalf("invoices created = %d, want 1", fake.addCount())
	}
	if payment.Id == "" {
		t.Error("no invoice id returned")
	}
}

// A second registration for the same order must reuse the existing faktura rather than
// issue another one — the guard that the capture / webhook / reconciler triggers rely on.
func TestRegisterInvoiceSkipsWhenFakturaExists(t *testing.T) {
	c, fake := newGuardTestClient(t)

	first, err := c.RegisterInvoice(context.Background(), guardTestParams("17098", 3))
	if err != nil {
		t.Fatalf("first RegisterInvoice: %v", err)
	}

	// A fresh params object: the second trigger holds no invoice id of its own.
	second, err := c.RegisterInvoice(context.Background(), guardTestParams("17098", 3))
	if err != nil {
		t.Fatalf("second RegisterInvoice: %v", err)
	}

	if fake.addCount() != 1 {
		t.Errorf("invoices created = %d, want 1", fake.addCount())
	}
	if second.Id != first.Id {
		t.Errorf("second registration returned id %q, want the existing %q", second.Id, first.Id)
	}
}

// A proforma shares the order's id_external but must never be mistaken for its faktura,
// nor block one from being issued.
func TestRegisterInvoiceIgnoresProforma(t *testing.T) {
	c, fake := newGuardTestClient(t)

	if _, err := c.RegisterProforma(context.Background(), guardTestParams("17098", 2)); err != nil {
		t.Fatalf("RegisterProforma: %v", err)
	}
	if _, err := c.RegisterInvoice(context.Background(), guardTestParams("17098", 2)); err != nil {
		t.Fatalf("RegisterInvoice: %v", err)
	}

	if fake.addCount() != 2 {
		t.Fatalf("documents created = %d, want 2 (proforma + faktura)", fake.addCount())
	}
	if fake.added[1].Type != string(invoiceNormal) {
		t.Errorf("second document type = %q, want normal", fake.added[1].Type)
	}
}

// Concurrent triggers for one order — the capture goroutine and the webhook arriving
// together — must still produce a single faktura.
func TestRegisterInvoiceConcurrentTriggersCreateOne(t *testing.T) {
	c, fake := newGuardTestClient(t)

	const callers = 6
	var wg sync.WaitGroup
	ids := make([]string, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			payment, err := c.RegisterInvoice(context.Background(), guardTestParams("17098", 3))
			if err != nil {
				t.Errorf("caller %d: %v", i, err)
				return
			}
			ids[i] = payment.Id
		}(i)
	}
	wg.Wait()

	if fake.addCount() != 1 {
		t.Fatalf("invoices created = %d, want 1", fake.addCount())
	}
	for i, id := range ids {
		if id != ids[0] {
			t.Errorf("caller %d got invoice id %q, want %q", i, id, ids[0])
		}
	}
}

// Different orders must not be serialized against each other, and each gets its own
// faktura.
func TestRegisterInvoiceSeparateOrders(t *testing.T) {
	c, fake := newGuardTestClient(t)

	for _, orderId := range []string{"17098", "17099", "17100"} {
		if _, err := c.RegisterInvoice(context.Background(), guardTestParams(orderId, 2)); err != nil {
			t.Fatalf("order %s: %v", orderId, err)
		}
	}
	if fake.addCount() != 3 {
		t.Errorf("invoices created = %d, want 3", fake.addCount())
	}
}

// splitItemCount produces an order large enough to be split, since orders below
// softInvoiceLimit are always issued as a single document.
const splitItemCount = softInvoiceLimit + 30 // 250 items → parts of 200 and 50

// A large order is issued as several parts under one id_external — these are not
// duplicates, and every part must be created.
func TestRegisterInvoiceCreatesAllSplitParts(t *testing.T) {
	c, fake := newGuardTestClient(t)

	payment, err := c.RegisterInvoice(context.Background(), guardTestParams("17098", splitItemCount))
	if err != nil {
		t.Fatalf("RegisterInvoice: %v", err)
	}
	if fake.addCount() != 2 {
		t.Fatalf("invoices created = %d, want 2", fake.addCount())
	}
	if len(payment.Parts) != 2 {
		t.Errorf("payment parts = %d, want 2", len(payment.Parts))
	}
	if n := len(fake.added[0].Contents); n != maxInvoiceItems {
		t.Errorf("part 1 lines = %d, want %d", n, maxInvoiceItems)
	}
	if n := len(fake.added[1].Contents); n != splitItemCount-maxInvoiceItems {
		t.Errorf("part 2 lines = %d, want %d", n, splitItemCount-maxInvoiceItems)
	}
	if !strings.Contains(fake.added[1].Description, "część 2/2") {
		t.Errorf("part 2 description = %q, want a 2/2 marker", fake.added[1].Description)
	}
}

// The case the guard must not get wrong: a split order whose second part failed. The
// re-run has to finish the missing part — not treat the order as already invoiced, and
// not re-issue the part that succeeded.
func TestRegisterInvoiceResumesIncompleteSplit(t *testing.T) {
	c, fake := newGuardTestClient(t)

	fake.failFrom = 2 // part 1 succeeds, part 2 is rejected
	_, err := c.RegisterInvoice(context.Background(), guardTestParams("17098", splitItemCount))
	if err == nil {
		t.Fatal("expected the split to fail on part 2")
	}
	if !strings.Contains(err.Error(), "1 already registered") {
		t.Errorf("error %q does not report the registered part", err)
	}
	if fake.addCount() != 1 {
		t.Fatalf("invoices after failed run = %d, want 1", fake.addCount())
	}

	fake.failFrom = 0 // wFirma recovers; the retry queue re-runs the job
	payment, err := c.RegisterInvoice(context.Background(), guardTestParams("17098", splitItemCount))
	if err != nil {
		t.Fatalf("resumed RegisterInvoice: %v", err)
	}

	if fake.addCount() != 2 {
		t.Fatalf("invoices after resume = %d, want 2", fake.addCount())
	}
	created := fake.added[1]
	if n := len(created.Contents); n != splitItemCount-maxInvoiceItems {
		t.Errorf("resumed part lines = %d, want %d (the missing part, not a repeat)", n, splitItemCount-maxInvoiceItems)
	}
	if !strings.Contains(created.Description, "część 2/2") {
		t.Errorf("resumed part description = %q, want a 2/2 marker", created.Description)
	}
	if len(payment.Parts) != 2 {
		t.Errorf("payment parts = %d, want 2", len(payment.Parts))
	}
}

// Once every part of a split order exists, a further trigger must create nothing.
func TestRegisterInvoiceSkipsCompleteSplit(t *testing.T) {
	c, fake := newGuardTestClient(t)

	if _, err := c.RegisterInvoice(context.Background(), guardTestParams("17098", splitItemCount)); err != nil {
		t.Fatalf("first RegisterInvoice: %v", err)
	}
	if _, err := c.RegisterInvoice(context.Background(), guardTestParams("17098", splitItemCount)); err != nil {
		t.Fatalf("second RegisterInvoice: %v", err)
	}
	if fake.addCount() != 2 {
		t.Errorf("invoices created = %d, want 2 (the split parts only)", fake.addCount())
	}
}
