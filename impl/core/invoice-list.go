package core

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"wfsync/entity"
	"wfsync/internal/wfirma"
	"wfsync/lib/sl"
)

// InvoiceList reports which orders in a date range are registered in wFirma — the
// analytics view behind GET /v1/wf/list.
//
// Rows are keyed by the order's external reference (entity.CheckoutParams.ExternalRef),
// the value wFirma stores in id_external. That key, not the order id, is what identifies
// an order across systems: the B2B portal has its own id space and keys on the order UID
// while its OrderId stays the human order number.
//
// Three sources are merged, each contributing what it alone knows and, for shared fields,
// in descending trust order:
//
//   - OpenCart — order facts (status, client, amounts) for shop orders;
//   - checkout_params — identity, origin and payment method for orders from every source,
//     including those with no OpenCart row and those whose invoice is still in the retry
//     queue and would otherwise be invisible;
//   - wFirma — the registration itself: number, date, and whether it is an accepted
//     faktura or a KSeF draft.
//
// A failing source is recorded in Sources instead of aborting the report, so an empty
// Items list always means "nothing matched" and never "a source was down".
func (c *Core) InvoiceList(ctx context.Context, from, to string) (*entity.InvoiceListResult, error) {
	if c.inv == nil {
		return nil, fmt.Errorf("invoice service not connected")
	}
	result := &entity.InvoiceListResult{From: from, To: to}

	// Step 1: wFirma documents registered in the range, grouped by id_external. A split
	// order produces several parts sharing one id_external, hence a slice per key.
	wfInvoices, err := c.inv.FindInvoices(ctx, from, to)
	if err != nil {
		c.log.With(sl.Err(err)).Warn("find wfirma invoices for list")
		wfInvoices = nil
	}
	result.AddSource("wfirma", len(wfInvoices), err)

	wfByRef := make(map[string][]*entity.LocalInvoice)
	var wfOrphans []*entity.LocalInvoice
	for _, inv := range wfInvoices {
		ref := inv.IdExternal
		if ref == "" {
			// Documents created before id_external was stamped still name their order in
			// the description we generate. Recovering the ref from there keeps them out of
			// the orphan bucket, where they would make their order read as never invoiced.
			ref = orderRefFromDescription(inv.Description)
		}
		if ref == "" {
			// Registered directly in wFirma — it belongs to no order we can name, but
			// still counts as a document.
			wfOrphans = append(wfOrphans, inv)
			continue
		}
		wfByRef[ref] = append(wfByRef[ref], inv)
	}

	// Step 2: OpenCart orders placed in the range.
	var ocOrders []*entity.OrderSummary
	if c.oc != nil {
		ocOrders, err = c.oc.GetOrdersByDateRange(from, to)
		if err != nil {
			c.log.With(sl.Err(err)).Warn("get opencart orders for list")
			ocOrders = nil
		}
		result.AddSource("opencart", len(ocOrders), err)
	}

	// Step 3: checkout params — those created in the range, plus any referenced by an
	// invoice or order in the range but created earlier (an order invoiced the following
	// month still needs its identity resolved).
	cpByRef, cpByOrderId := c.checkoutParamsForList(result, from, to, listRefs(ocOrders, wfByRef))

	// Step 4: merge into one row per external reference.
	rows := newRowSet()

	for _, oc := range ocOrders {
		// Resolve the order id to its external reference when checkout params know of one.
		ref := oc.OrderId
		if cp, ok := cpByOrderId[oc.OrderId]; ok {
			ref = cp.ExternalRef()
		}
		item := rows.get(ref)
		item.OrderId = oc.OrderId
		item.OrderStatus = oc.OrderStatus
		item.OrderDate = oc.DateAdded.Format(entity.DateLayout)
		item.ContractorName = oc.ClientName
		item.Source = entity.SourceOpenCart
		item.IsB2B = wfirma.IsB2BCustomerGroup(oc.CustomerGroup)
		item.InvoiceId = oc.InvoiceId
		item.SetTotal(oc.Currency, oc.Total)
	}

	for ref, cp := range cpByRef {
		item := rows.get(ref)
		if item.OrderId == "" {
			item.OrderId = cp.OrderId
		}
		item.ExternalId = cp.ExternalId
		if item.Source == "" {
			item.Source = cp.Source
		}
		if item.OrderDate == "" && !cp.Created.IsZero() {
			item.OrderDate = cp.Created.Format(entity.DateLayout)
		}
		if item.ContractorName == "" && cp.ClientDetails != nil {
			item.ContractorName = cp.ClientDetails.Name
		}
		if item.InvoiceId == "" {
			item.InvoiceId = cp.InvoiceId
		}
		// A checkout session is what makes an order a Stripe order; the wFirma-only
		// flows (B2B, order-to-invoice, manual payload) write params without one.
		if cp.SessionId != "" {
			item.IsStripe = true
		}
		if !item.IsB2B {
			item.IsB2B = cp.Source == entity.SourceB2B || wfirma.IsB2BCustomerGroup(cp.CustomerGroup)
		}
		item.SetTotal(cp.Currency, cp.Total)
	}

	for ref, invoices := range wfByRef {
		item := rows.get(ref)
		if item.OrderId == "" {
			item.OrderId = ref
		}
		applyInvoices(item, invoices)
	}

	items := rows.items()
	for _, inv := range wfOrphans {
		item := &entity.InvoiceListItem{}
		applyInvoices(item, []*entity.LocalInvoice{inv})
		items = append(items, item)
	}

	for _, item := range items {
		item.Date = item.OrderDate
		if item.Date == "" {
			item.Date = item.InvoiceDate
		}
		result.Summary.Orders++
		switch item.DocumentType {
		case entity.DocumentInvoice:
			result.Summary.Invoiced++
		case entity.DocumentDraft:
			result.Summary.Drafts++
		default:
			result.Summary.NotInvoiced++
		}
	}

	// Sort by display date, then order id, so the report is reproducible run to run.
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Date != items[j].Date {
			return items[i].Date < items[j].Date
		}
		return items[i].OrderId < items[j].OrderId
	})
	result.Items = items

	return result, nil
}

// checkoutParamsForList loads the checkout params relevant to the report and indexes
// them by external reference and by order id. The order-id index deliberately excludes
// B2B documents: their OrderId is a portal order number from a separate id space that
// can collide with an OpenCart order id, which is the very reason they carry a separate
// external id.
func (c *Core) checkoutParamsForList(result *entity.InvoiceListResult, from, to string, refs []string) (map[string]*entity.CheckoutParams, map[string]*entity.CheckoutParams) {
	byRef := make(map[string]*entity.CheckoutParams)
	byOrderId := make(map[string]*entity.CheckoutParams)
	if c.db == nil {
		return byRef, byOrderId
	}

	params, err := c.db.GetCheckoutParamsByDateRange(from, to)
	if err != nil {
		c.log.With(sl.Err(err)).Warn("get checkout params by date range for list")
	}
	referenced, refErr := c.db.GetCheckoutParamsByRefs(refs)
	if refErr != nil {
		c.log.With(sl.Err(refErr)).Warn("get checkout params by refs for list")
		if err == nil {
			err = refErr
		}
	}
	params = append(params, referenced...)
	result.AddSource("checkout_params", len(params), err)

	for _, cp := range params {
		ref := cp.ExternalRef()
		if ref == "" {
			continue
		}
		// The same order can hold several documents (e.g. a re-issued hold); keep the
		// most recently touched one.
		if prev, ok := byRef[ref]; ok && !newerParams(cp, prev) {
			continue
		}
		byRef[ref] = cp
		if cp.Source != entity.SourceB2B && cp.OrderId != "" {
			byOrderId[cp.OrderId] = cp
		}
	}
	return byRef, byOrderId
}

// newerParams reports whether a describes a later state of the order than b, preferring
// the latest modification and falling back to creation time for never-updated documents.
func newerParams(a, b *entity.CheckoutParams) bool {
	if !a.Modified.Equal(b.Modified) {
		return a.Modified.After(b.Modified)
	}
	return a.Created.After(b.Created)
}

// descriptionOrderPrefix is the invoice description this service writes, e.g.
// "Numer zamówienia: 17098" or "Numer zamówienia: 17098 (część 1/2)" for a split order.
const descriptionOrderPrefix = "Numer zamówienia:"

// orderRefFromDescription extracts the order reference from an invoice description,
// returning "" for any description this service did not write. It is a fallback for
// documents with no id_external; the stamped field always wins when present.
func orderRefFromDescription(description string) string {
	_, after, found := strings.Cut(description, descriptionOrderPrefix)
	if !found {
		return ""
	}
	// Drop the part suffix a split order carries.
	if idx := strings.Index(after, "("); idx >= 0 {
		after = after[:idx]
	}
	return strings.TrimSpace(after)
}

// listRefs collects the lookup keys for the checkout params query: OpenCart order ids
// and the id_external of every invoice found in the range.
func listRefs(ocOrders []*entity.OrderSummary, wfByRef map[string][]*entity.LocalInvoice) []string {
	refs := make([]string, 0, len(ocOrders)+len(wfByRef))
	for _, o := range ocOrders {
		if o.OrderId != "" {
			refs = append(refs, o.OrderId)
		}
	}
	for ref := range wfByRef {
		refs = append(refs, ref)
	}
	return refs
}

// applyInvoices folds every wFirma document registered for one order into its row.
// Split orders produce several parts under a single id_external: their numbers are
// listed together, their amounts summed, and InvoiceParts records the count, so the
// order appears exactly once whether or not it also has an OpenCart row.
//
// An order counts as invoiced when any part is an accepted faktura; it is reported as a
// draft only when every part is still a draft awaiting manual acceptance in wFirma.
func applyInvoices(item *entity.InvoiceListItem, invoices []*entity.LocalInvoice) {
	if len(invoices) == 0 {
		return
	}
	sort.SliceStable(invoices, func(i, j int) bool {
		if invoices[i].Date != invoices[j].Date {
			return invoices[i].Date < invoices[j].Date
		}
		return invoices[i].Number < invoices[j].Number
	})

	var (
		numbers  []string
		totals   = make(map[string]int64)
		docType  = entity.DocumentDraft
		currency string
	)
	for _, inv := range invoices {
		if entity.DocumentTypeFor(inv.Type) == entity.DocumentInvoice {
			docType = entity.DocumentInvoice
		}
		if inv.Number != "" {
			numbers = append(numbers, inv.Number)
		}
		if item.ContractorName == "" && inv.Contractor != nil {
			item.ContractorName = inv.Contractor.Name
		}
		if inv.Currency != "" {
			currency = inv.Currency
			totals[inv.Currency] += toCents(inv.Total)
		}
	}

	item.Invoiced = true
	item.DocumentType = docType
	item.InvoiceParts = len(invoices)
	item.InvoiceNumber = strings.Join(numbers, ", ")
	item.InvoiceDate = invoices[0].Date
	if item.InvoiceId == "" {
		item.InvoiceId = invoices[0].Id
	}
	// Only fills in when no order-side amount is known — see InvoiceListItem.SetTotal.
	if len(totals) == 1 {
		item.SetTotal(currency, totals[currency])
	}
}

// toCents converts a wFirma decimal amount to minor units, rounding rather than
// truncating (12.34 * 100 is 1233.999… in binary floating point).
func toCents(v float64) int64 {
	return int64(math.Round(v * 100))
}

// rowSet accumulates one list item per external reference while preserving insertion
// order, so rows keep the order of the source that introduced them.
type rowSet struct {
	byRef map[string]*entity.InvoiceListItem
	order []string
}

func newRowSet() *rowSet {
	return &rowSet{byRef: make(map[string]*entity.InvoiceListItem)}
}

// get returns the row for ref, creating it on first use.
func (r *rowSet) get(ref string) *entity.InvoiceListItem {
	if item, ok := r.byRef[ref]; ok {
		return item
	}
	item := &entity.InvoiceListItem{}
	r.byRef[ref] = item
	r.order = append(r.order, ref)
	return item
}

func (r *rowSet) items() []*entity.InvoiceListItem {
	items := make([]*entity.InvoiceListItem, 0, len(r.order))
	for _, ref := range r.order {
		items = append(items, r.byRef[ref])
	}
	return items
}
