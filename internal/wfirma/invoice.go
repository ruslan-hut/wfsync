package wfirma

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"wfsync/entity"
	"wfsync/lib/sl"

	"github.com/google/uuid"
)

// invoiceType maps to the wFirma "type" field.
// See entity.go header for all supported values.
type invoiceType string

const (
	invoiceProforma invoiceType = "proforma" // proforma invoice (przedpłata)
	invoiceNormal   invoiceType = "normal"   // standard VAT invoice (faktura VAT)

	// invoiceNormalDraft is a draft VAT invoice (wersja robocza faktury, "WRF").
	// Unlike invoiceNormal, a draft is NOT auto-submitted to KSeF on creation, so it
	// can be registered even when the API user has no KSeF authorization. It is not yet
	// an accounting document (no number, no KSeF UID) and must be accepted manually in
	// wFirma to become a legal invoice. Used only as a fallback — see submitInvoice.
	invoiceNormalDraft invoiceType = "normal_draft"

	// defaultPaymentMethod is used for all created invoices.
	// Supported values: "transfer", "cash", "compensation", "cod", "payment_card".
	defaultPaymentMethod = "transfer"

	// defaultPaymentDays is the number of days from the invoice date until payment is due.
	defaultPaymentDays = 7

	// shippingVatCode overrides the VAT code for shipping line items.
	// When empty, shipping uses the same VAT code as goods.
	// Set to a specific code (e.g. "NP", "ZW", "23") to tax shipping differently.
	shippingVatCode = ""

	// shippingSku is the default SKU used for shipping line items when no SKU is set.
	// Used to look up the wFirma good ID for shipping costs.
	shippingSku = "Zwrot"
)

// RegisterInvoice creates a standard VAT invoice (faktura VAT) in wFirma.
func (c *Client) RegisterInvoice(ctx context.Context, params *entity.CheckoutParams) (*entity.Payment, error) {
	if !c.enabled {
		return nil, fmt.Errorf("wFirma is disabled")
	}
	return c.invoice(ctx, invoiceNormal, params)
}

// RegisterProforma creates a proforma invoice in wFirma.
func (c *Client) RegisterProforma(ctx context.Context, params *entity.CheckoutParams) (*entity.Payment, error) {
	if !c.enabled {
		return nil, fmt.Errorf("wFirma is disabled")
	}
	return c.invoice(ctx, invoiceProforma, params)
}

// softInvoiceLimit is the threshold below which an order is sent as a single invoice
// even if it exceeds maxInvoiceItems. Orders with fewer than softInvoiceLimit items
// are never split; orders at or above it are split into chunks of maxInvoiceItems.
const (
	maxInvoiceItems  = 200
	softInvoiceLimit = 220
)

// invoice builds and sends an invoices/add request to the wFirma API.
// Flow: validate params → find/create contractor → build invoice with contents → POST to API → persist result.
// Orders with more than maxInvoiceItems line items are automatically split into
// multiple invoices, each annotated with a part number in the description.
func (c *Client) invoice(ctx context.Context, invType invoiceType, params *entity.CheckoutParams) (payment *entity.Payment, err error) {
	log := c.log.With(slog.String("session_id", params.SessionId), slog.String("order_id", params.OrderId))
	defer func() {
		if r := recover(); r != nil {
			log.Error("panic recovered in RegisterInvoice", slog.Any("panic", r))
			payment = nil
			err = fmt.Errorf("panic in invoice creation: %v", r)
		}
	}()
	// Serialize faktura creation for this order across the concurrent triggers that can
	// observe the same capture (capture API goroutine, Stripe webhook, reconciler, retry
	// queue) so the duplicate check below cannot be overtaken between find and add.
	// Proformas are exempt: they are intentionally re-issued when an order changes.
	if invType != invoiceProforma {
		defer c.orderLocks.lock(params.ExternalRef())()
	}

	if c.db != nil {
		err := c.db.SaveCheckoutParams(params)
		if err != nil {
			log.Error("save checkout params", sl.Err(err))
		}
	}

	if err = params.Validate(); err != nil {
		return nil, fmt.Errorf("invalid checkout params: %w", err)
	}

	existing, err := c.getContractor(ctx, params.ClientDetails.Email)
	if err != nil {
		return nil, fmt.Errorf("contractor: %w", err)
	}
	var contractorID string
	if existing == nil {
		contractorID, err = c.createContractor(ctx, params.ClientDetails)
		if err != nil {
			return nil, fmt.Errorf("create contractor: %w", err)
		}
	} else {
		contractorID = existing.ID
		if params.ClientDetails.TaxId == "" && existing.Nip != "" {
			// Returning customer already registered as B2B on wFirma side but the current
			// order omits a tax ID. Promote to B2B so EU VAT rules (WDT / Polish 23% / EXP)
			// apply consistently — without this the order would fall under OSS as B2C.
			params.ClientDetails.TaxId = existing.Nip
			if !b2bCustomerGroups[params.CustomerGroup] {
				params.CustomerGroup = -1
			}
			log.Info("promoted to B2B from wFirma stored tax id",
				slog.String("tax_id", existing.Nip),
				slog.String("contractor_id", contractorID),
				slog.String("email", params.ClientDetails.Email))
		}
		// The invoice references the contractor by ID only, so wFirma prints whatever
		// address is on the record. Push the current order's address and tax ID before
		// issuing the document, or a customer who moved keeps getting stale invoices.
		if err := c.syncContractor(ctx, existing, params.ClientDetails); err != nil {
			log.Warn("sync contractor", sl.Err(err))
		}
	}
	log = log.With(slog.String("contractor_id", contractorID))

	contractor := &Contractor{
		ID: contractorID,
	}

	countryCode := params.ClientDetails.CountryCode()
	hasTaxId := params.ClientDetails.TaxId != ""
	isB2B := b2bCustomerGroups[params.CustomerGroup]
	opencartRate := params.TaxRate()

	// VIES validation: check the TaxId against the EU VIES service.
	// Non-blocking — the result is logged but does not change hasTaxId or prevent invoice creation.
	if hasTaxId && c.vies != nil {
		switch c.vies.ValidateTaxId(params.ClientDetails.TaxId, countryCode) {
		case entity.VIESValid:
			log.Debug("VIES validation passed",
				slog.String("tax_id", params.ClientDetails.TaxId),
				slog.String("country", countryCode))
		case entity.VIESInvalid:
			log.With(
				slog.String("tax_id", params.ClientDetails.TaxId),
				slog.String("country", countryCode),
				slog.String("email", params.ClientDetails.Email),
				slog.String("name", params.ClientDetails.Name),
				slog.String("tg_topic", entity.TopicError),
			).Warn("VIES validation failed")
		case entity.VIESInconclusive:
			// VIES was unavailable or rate-limited — not a verdict on the number.
			log.Debug("VIES validation inconclusive (service unavailable)",
				slog.String("tax_id", params.ClientDetails.TaxId),
				slog.String("country", countryCode))
		}
	}

	// Use the dynamic VAT provider only when it has been verified against the DB.
	// Otherwise fall back to the hardcoded euCountries map.
	var vp VATProvider
	if c.vatRates != nil && c.vatRates.Verified() {
		vp = c.vatRates
	}

	goodsVat := resolveGoodsVatCode(opencartRate, countryCode, hasTaxId, isB2B, vp)

	// Cross-check OpenCart's calculated rate against our internal VAT rate database.
	// Only meaningful for EU B2C orders where the destination-country rate is used.
	// Internal rate takes priority over OpenCart data because OpenCart's tax config can
	// be wrong. The internal rate is validated against the curated baseline on load (see
	// internal/vatrates.reconcile), so a stale external feed can no longer override a
	// correct OpenCart rate with an outdated one.
	if vp != nil && !isB2B && countryCode != "" && countryCode != "PL" {
		if internalRate := vp.GetStandardRate(countryCode); internalRate > 0 && internalRate != float64(opencartRate) {
			log.Warn("VAT rate mismatch: using internal",
				slog.String("country", countryCode),
				slog.Int("opencart_rate", opencartRate),
				slog.Float64("internal_rate", internalRate),
				slog.Int64("total", params.Total),
				slog.Int64("tax_value", params.TaxValue),
				slog.Int64("shipping", params.Shipping),
				slog.String("tax_title", params.TaxTitle),
				slog.Int("customer_group", params.CustomerGroup),
				slog.Bool("has_tax_id", hasTaxId),
				slog.String("email", params.ClientDetails.Email),
				slog.String("tg_topic", entity.TopicError),
			)
			goodsVat = strconv.FormatFloat(internalRate, 'f', -1, 64)
		}
	}

	// Determine if this is an EU OSS sale (B2C to another EU country).
	// OSS invoices use foreign vat_code IDs (resolved via declaration_countries).
	isEU := false
	if vp != nil {
		isEU = vp.IsEUCountry(countryCode)
	} else {
		isEU = euCountries[countryCode]
	}
	isOSS := !isB2B && isEU && countryCode != "" && countryCode != "PL"

	// Pre-resolve distinct vat codes to wFirma IDs once per invoice.
	// For OSS, resolve the foreign vat_code ID via declaration_countries → vat_codes chain.
	// For non-OSS, resolve Polish vat_code IDs by code name.
	vatCodeIDCache := make(map[string]string)
	var ossVatCodeID string
	if isOSS {
		ossVatCodeID = c.resolveOSSVatCodeIDWithRate(ctx, countryCode, goodsVat)
		if ossVatCodeID == "" {
			log.Warn("OSS vat_code not found, falling back to plain vat field",
				slog.String("country", countryCode),
				slog.String("rate", goodsVat))
		}
	} else {
		for _, code := range []string{goodsVat, shippingVatCode} {
			if code == "" {
				continue
			}
			if _, ok := vatCodeIDCache[code]; !ok {
				vatCodeIDCache[code] = c.resolveVatCodeID(ctx, code)
			}
		}
	}

	var contents []*ContentLine
	for _, line := range params.LineItems {
		vatCode := goodsVat
		if line.Shipping && shippingVatCode != "" {
			vatCode = shippingVatCode
		}
		content := &Content{
			Name:  line.Name,
			Count: line.Qty,
			Price: float64(line.Price) / 100.0,
			Unit:  "szt.",
		}
		// For OSS invoices, use the foreign vat_code ID resolved via declaration_countries.
		// Falls back to plain "vat" field if the foreign vat_code was not found.
		if isOSS && ossVatCodeID != "" {
			content.VatCode = &VatCodeRef{ID: ossVatCodeID}
		} else if !isOSS {
			if vcID := vatCodeIDCache[vatCode]; vcID != "" {
				content.VatCode = &VatCodeRef{ID: vcID}
			} else {
				content.Vat = vatCode
			}
		} else {
			content.Vat = vatCode
		}
		sku := line.Sku
		if sku == "" && line.Shipping {
			sku = shippingSku
		}
		if sku != "" {
			content.Good = c.resolveGoodId(ctx, sku)
		}
		contents = append(contents, &ContentLine{
			Content: content,
		})
	}

	now := time.Now()
	issueDate := now.Format("2006-01-02")
	disposalDate := params.Created.Format("2006-01-02")
	paymentDate := now.AddDate(0, 0, defaultPaymentDays).Format("2006-01-02")

	// Split contents into chunks of maxInvoiceItems.
	chunks := chunkContents(contents, maxInvoiceItems, softInvoiceLimit)
	totalParts := len(chunks)

	var firstPayment *entity.Payment
	var parts []*entity.Payment

	// Order-level duplicate guard, applied here because every source of a faktura — the
	// Stripe webhook, the capture API, the reconciler, the retry queue, the order-to-invoice
	// and payload endpoints — funnels through this function. Callers that guard themselves
	// stay correct; callers that cannot (they hold no invoice id, or their local record of
	// one failed to save) are covered by this check.
	//
	// Existing documents also make the creation loop resumable: n fakturas for this order
	// mean chunks 1..n were already registered, so a split order that died part-way finishes
	// its remaining parts instead of being written off as "already invoiced". Chunking is
	// deterministic for the same params, so index n maps to the next missing part.
	startIdx := 0
	if invType != invoiceProforma && params.ExternalRef() != "" {
		existing, findErr := c.findFakturasByExternalId(ctx, params.ExternalRef())
		if findErr != nil {
			// State unknown — abort rather than risk a duplicate.
			return nil, fmt.Errorf("check existing faktura for order %s: %w", params.OrderId, findErr)
		}
		for i, ex := range existing {
			if i >= totalParts {
				break
			}
			amount := contentsTotal(chunks[i])
			// The amount a part should carry is known from its chunk, so a mismatch means
			// the existing documents are not the parts of this order laid out in order —
			// most likely a duplicate among them. Creation still stops (a faktura cannot be
			// unissued), but the order is flagged for review rather than silently accepted.
			if !sameAmount(ex.Total, amount) {
				log.With(
					slog.String("invoice_id", ex.Id),
					slog.String("external_id", params.ExternalRef()),
					slog.String("part", fmt.Sprintf("%d/%d", i+1, totalParts)),
					slog.String("registered_total", fmt.Sprintf("%.2f", ex.Total)),
					slog.String("expected_total", fmt.Sprintf("%.2f", amount)),
					slog.String("tg_topic", entity.TopicError),
				).Warn("registered faktura amount does not match the order part, check for duplicates")
			}
			parts = append(parts, &entity.Payment{
				Amount:  int64(amount * 100),
				Id:      ex.Id,
				OrderId: params.OrderId,
			})
		}
		if len(existing) > totalParts {
			log.With(
				slog.String("external_id", params.ExternalRef()),
				slog.Int("registered", len(existing)),
				slog.Int("parts", totalParts),
				slog.String("tg_topic", entity.TopicError),
			).Warn("order holds more fakturas than it has parts, check for duplicates")
		}
		startIdx = len(parts)
		if startIdx > 0 {
			log.With(
				slog.String("invoice_id", parts[0].Id),
				slog.String("external_id", params.ExternalRef()),
				slog.String("parts", fmt.Sprintf("%d/%d", startIdx, totalParts)),
			).Info("faktura already exists for order, skipping creation")
		}
	}

	for partIdx := startIdx; partIdx < totalParts; partIdx++ {
		chunk := chunks[partIdx]
		partNum := partIdx + 1

		// Calculate the total for this chunk from its line items.
		chunkTotal := contentsTotal(chunk)

		description := "Numer zamówienia: " + params.OrderId
		if totalParts > 1 {
			description = fmt.Sprintf("Numer zamówienia: %s (część %d/%d)", params.OrderId, partNum, totalParts)
		}

		inv := &Invoice{
			Contractor:    contractor,
			Type:          string(invType),
			PriceType:     "brutto",
			PaymentMethod: defaultPaymentMethod,
			PaymentDate:   paymentDate,
			DisposalDate:  disposalDate,
			Total:         chunkTotal,
			IdExternal:    params.ExternalRef(),
			Description:   description,
			Date:          issueDate,
			Currency:      strings.ToUpper(params.Currency),
			Contents:      chunk,
		}

		if isOSS {
			inv.VatMossDetails = buildVatMossDetails(params.ClientDetails, countryCode)
		}

		// Select the wFirma company account by invoice currency from the local
		// bank-account cache (populated by SyncBankAccounts, curated via the
		// is_allowed flag). If none is marked allowed for this currency, omit
		// the field — wFirma uses its default account.
		if c.db != nil {
			if ba, dbErr := c.db.GetAllowedBankAccount(strings.ToUpper(params.Currency)); dbErr != nil {
				log.Warn("lookup allowed bank account", sl.Err(dbErr))
			} else if ba != nil {
				inv.CompanyAccount = &CompanyAccountRef{ID: ba.ID}
			}
		}

		resultInv, err := c.submitInvoice(ctx, log, inv, chunk)
		if err != nil {
			// A split order that fails part-way leaves the earlier parts registered in
			// wFirma. Say so in the error: the order is neither uninvoiced nor complete,
			// and a re-run resumes at this part rather than duplicating the earlier ones.
			if len(parts) > 0 {
				return nil, fmt.Errorf("part %d/%d (%d already registered, re-run to resume): %w",
					partNum, totalParts, len(parts), err)
			}
			return nil, err
		}

		inv.Id = resultInv.Id
		inv.Number = resultInv.Number

		if c.db != nil {
			if saveErr := c.db.SaveInvoice(inv.Id, inv); saveErr != nil {
				log.Error("save invoice", sl.Err(saveErr))
			}
		}

		c.log.With(
			slog.String("wfirma_id", inv.Id),
			slog.String("wfirma_number", inv.Number),
			slog.String("order_id", params.OrderId),
			slog.String("total", fmt.Sprintf("%.2f", chunkTotal)),
			slog.String("tax", func() string {
				if isOSS {
					return goodsVat + " OSS"
				}
				return goodsVat
			}()),
			slog.String("email", params.ClientDetails.Email),
			slog.String("name", params.ClientDetails.Name),
			slog.String("country", params.ClientDetails.Country),
			slog.String("tax_id", params.ClientDetails.TaxId),
			slog.String("customer_group", formatCustomerGroup(params.CustomerGroup)),
			slog.String("currency", params.Currency),
			slog.String("part", fmt.Sprintf("%d/%d", partNum, totalParts)),
			slog.String("tg_topic", entity.TopicInvoice),
		).Info("invoice created")

		parts = append(parts, &entity.Payment{
			Amount:  int64(chunkTotal * 100),
			Id:      inv.Id,
			OrderId: params.OrderId,
		})
	}

	// The returned head payment mirrors the first part. It is a DISTINCT struct from
	// parts[0]: if the head were also an element of its own Parts slice, JSON encoding
	// would recurse into itself and fail with "encountered a cycle via *entity.Payment".
	if len(parts) > 0 {
		head := *parts[0]
		firstPayment = &head
	}

	// Expose all parts only when the order was actually split, so single-document
	// responses stay backward compatible (no extra parts payload).
	if firstPayment != nil && len(parts) > 1 {
		firstPayment.Parts = parts
	}

	// Persist the first invoice ID back to checkout params. The id is set on params even
	// without a database so callers still learn which document the order resolved to —
	// including when creation was skipped because it already existed.
	if firstPayment != nil {
		if invType == invoiceProforma {
			params.ProformaId = firstPayment.Id
		} else {
			params.InvoiceId = firstPayment.Id
		}
	}
	if c.db != nil && firstPayment != nil {
		if err := c.db.UpdateCheckoutParams(params); err != nil {
			log.Error("update checkout params", sl.Err(err))
		}
	}

	return firstPayment, nil
}

// submitInvoice sends an invoices/add request and handles error responses,
// including automatic retry without Good references on stock errors.
func (c *Client) submitInvoice(ctx context.Context, log *slog.Logger, inv *Invoice, contents []*ContentLine) (*InvoiceData, error) {
	addPayload := map[string]interface{}{
		"api": map[string]interface{}{
			"invoices": []map[string]interface{}{
				{"invoice": inv},
			},
		},
	}

	addRes, err := c.request(ctx, "invoices", "add", addPayload)
	if err != nil {
		log.Error("add invoice", sl.Err(err))
		return nil, fmt.Errorf("add invoice: %w", err)
	}

	var addResp InvoiceResponse
	if err = json.Unmarshal(addRes, &addResp); err != nil {
		log.With(slog.String("response", truncateBody(string(addRes)))).Warn("unmarshal invoice response")
		return nil, fmt.Errorf("unmarshal invoice response: %w", err)
	}

	// On retry-queue attempts, keep failures local: the original error was already
	// reported to Telegram when the job was enqueued, so per-attempt alerts are just
	// noise. tgAttr toggles the dispatch accordingly. The (truncated) response body is
	// logged on every attempt, retry included: a failure that only ever recurs on retries
	// is otherwise undiagnosable, which is exactly when the body is needed most.
	isRetry := entity.IsRetry(ctx)
	tgAttr := slog.String("tg_topic", entity.TopicError)
	if isRetry {
		tgAttr = slog.Bool("tg_skip", true)
	}

	if addResp.Status.Code == "ERROR" {
		errMsg := extractInvoiceErrors(&addResp, addRes)

		stockErrIdxs := extractStockErrorIndices(&addResp)
		if len(stockErrIdxs) > 0 {
			log.With(
				slog.String("error", errMsg),
				tgAttr,
			).Warn("stock error, retrying without good references")

			for _, idx := range stockErrIdxs {
				if idx < len(contents) {
					contents[idx].Content.Good = nil
				}
			}
			inv.Contents = contents

			addPayload = map[string]interface{}{
				"api": map[string]interface{}{
					"invoices": []map[string]interface{}{
						{"invoice": inv},
					},
				},
			}

			addRes, err = c.request(ctx, "invoices", "add", addPayload)
			if err != nil {
				log.Error("retry add invoice", sl.Err(err))
				return nil, fmt.Errorf("retry add invoice: %w", err)
			}

			addResp = InvoiceResponse{}
			if err = json.Unmarshal(addRes, &addResp); err != nil {
				return nil, fmt.Errorf("unmarshal retry invoice response: %w", err)
			}

			if addResp.Status.Code == "ERROR" {
				retryErrMsg := extractInvoiceErrors(&addResp, addRes)
				log.With(
					slog.String("error", retryErrMsg),
					slog.String("response", truncateBody(string(addRes))),
					tgAttr,
				).Warn("retry invoice creation error")
				return nil, fmt.Errorf("wFirma error (retry): %s", retryErrMsg)
			}
		} else if c.draftFallback && isKSefAuthError(errMsg) && inv.Type == string(invoiceNormal) {
			// wFirma blocked issuance because the API user has no KSeF authorization.
			// Re-submit the same document as a draft (wersja robocza), which is not sent
			// to KSeF and therefore succeeds. The draft is NOT a legal invoice yet — a
			// human must accept it in wFirma once KSeF authorization is restored.
			draftInv, draftErr := c.submitDraftFallback(ctx, log, inv, errMsg)
			if draftErr != nil {
				return nil, draftErr
			}
			return draftInv, nil
		} else {
			log.With(
				slog.String("error", errMsg),
				slog.String("response", truncateBody(string(addRes))),
				tgAttr,
			).Warn("invoice creation error")
			return nil, fmt.Errorf("wFirma error: %s", errMsg)
		}
	}

	var result InvoiceData
	if wrapper, ok := addResp.Invoices["0"]; ok {
		result = wrapper.Invoice
	}
	if result.Id == "" {
		log.With(slog.String("response", string(addRes))).Warn("no invoice id in response")
		return nil, fmt.Errorf("no invoice id returned from wFirma")
	}

	return &result, nil
}

// ksefAuthErrorPhrases are fragments of the wFirma error message returned when an
// invoice cannot be issued because the API user lacks KSeF authorization, e.g.
// "Brak autoryzacji w KSeF 2.0. Zautoryzuj się w zakładce PRZYCHODY » KSEF I INTEGRACJE".
// Matched case-insensitively so wording/version changes ("KSeF 2.0" → "KSeF") still hit.
var ksefAuthErrorPhrases = []string{"ksef"}

// isKSefAuthError reports whether a wFirma error message indicates the document was
// blocked due to missing KSeF authorization (as opposed to a payload/validation error).
func isKSefAuthError(msg string) bool {
	lower := strings.ToLower(msg)
	if !strings.Contains(lower, "autoryzac") { // "autoryzacji"/"autoryzuj" — authorization
		return false
	}
	for _, p := range ksefAuthErrorPhrases {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// submitDraftFallback re-issues an invoice as a draft (wersja robocza / "WRF") after the
// normal issuance was rejected for missing KSeF authorization. A draft is not transmitted
// to KSeF, so it registers successfully, but it is not yet a legal accounting document
// (it has no final number until accepted in wFirma). This always raises a Telegram alert —
// including on retry-queue attempts — because the resulting draft requires manual acceptance.
func (c *Client) submitDraftFallback(ctx context.Context, log *slog.Logger, inv *Invoice, origErr string) (*InvoiceData, error) {
	inv.Type = string(invoiceNormalDraft)

	draftPayload := map[string]interface{}{
		"api": map[string]interface{}{
			"invoices": []map[string]interface{}{
				{"invoice": inv},
			},
		},
	}

	res, err := c.request(ctx, "invoices", "add", draftPayload)
	if err != nil {
		log.Error("draft fallback add invoice", sl.Err(err))
		return nil, fmt.Errorf("draft fallback add invoice: %w", err)
	}

	var resp InvoiceResponse
	if err = json.Unmarshal(res, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal draft invoice response: %w", err)
	}

	if resp.Status.Code == "ERROR" {
		draftErrMsg := extractInvoiceErrors(&resp, res)
		log.With(
			slog.String("ksef_error", origErr),
			slog.String("draft_error", draftErrMsg),
			slog.String("tg_topic", entity.TopicError),
		).Error("KSeF draft fallback also failed")
		return nil, fmt.Errorf("wFirma error (draft fallback): %s", draftErrMsg)
	}

	var result InvoiceData
	if wrapper, ok := resp.Invoices["0"]; ok {
		result = wrapper.Invoice
	}
	if result.Id == "" {
		log.With(slog.String("response", string(res))).Warn("no invoice id in draft response")
		return nil, fmt.Errorf("no invoice id returned from wFirma (draft fallback)")
	}

	// Always alert: a draft was created instead of a real invoice and someone must
	// accept it in wFirma (PRZYCHODY » FAKTURY) once KSeF authorization is restored.
	log.With(
		slog.String("wfirma_id", result.Id),
		slog.String("ksef_error", origErr),
		slog.String("tg_topic", entity.TopicError),
	).Warn("KSeF authorization missing — invoice saved as DRAFT (wersja robocza), accept it manually in wFirma")

	return &result, nil
}

// chunkContents splits a slice of content lines into chunks of at most size elements.
// If the total number of items is below softLimit, no split is performed.
// contentsTotal sums the gross value of a chunk of invoice lines. Chunking is
// deterministic for given params, so this also yields the amount of an already-registered
// part when creation is resumed or skipped.
func contentsTotal(contents []*ContentLine) float64 {
	var total float64
	for _, cl := range contents {
		total += cl.Content.Price * float64(cl.Content.Count)
	}
	return total
}

func chunkContents(contents []*ContentLine, size, softLimit int) [][]*ContentLine {
	if len(contents) < softLimit {
		return [][]*ContentLine{contents}
	}
	var chunks [][]*ContentLine
	for i := 0; i < len(contents); i += size {
		end := i + size
		if end > len(contents) {
			end = len(contents)
		}
		chunks = append(chunks, contents[i:end])
	}
	return chunks
}

// truncateBody shortens a response body for logging, keeping the head and tail.
//
// The budget is generous because a wFirma error response echoes the whole submitted
// invoice and buries the field errors among the line items: a tight window drops the
// only part worth logging.
const (
	truncateBodyLimit = 4000
	truncateBodyEdge  = 1500
)

func truncateBody(s string) string {
	if len(s) <= truncateBodyLimit {
		return s
	}
	return s[:truncateBodyEdge] + " ... [truncated] ... " + s[len(s)-truncateBodyEdge:]
}

// extractInvoiceErrors collects all error messages from the invoice response:
// request-level, invoice-level, contractor-level, invoicecontent-level and
// vat_moss_detail-level validation errors. Every node the request sends must be
// covered here — an error on an unread node degrades to "unknown error", which is
// indistinguishable from a response that carried no diagnosis at all.
func extractInvoiceErrors(resp *InvoiceResponse, raw []byte) string {
	var msgs []string
	for _, ew := range resp.Errors {
		msgs = append(msgs, fmt.Sprintf("%s: %s", ew.Error.Field, ew.Error.Message))
	}
	for _, wrapper := range resp.Invoices {
		inv := wrapper.Invoice
		for _, ew := range inv.Errors {
			msgs = append(msgs, fmt.Sprintf("%s: %s", ew.Error.Field, ew.Error.Message))
		}
		if inv.Contractor != nil {
			for _, ew := range inv.Contractor.Errors {
				msgs = append(msgs, fmt.Sprintf("contractor.%s: %s", ew.Error.Field, ew.Error.Message))
			}
		}
		for idx, cw := range inv.InvoiceContents {
			for _, ew := range cw.InvoiceContent.Errors {
				msgs = append(msgs, fmt.Sprintf("invoicecontent[%s] %q: %s: %s",
					idx, cw.InvoiceContent.Name, ew.Error.Field, ew.Error.Message))
			}
		}
		if inv.VatMossDetails != nil {
			for _, d := range inv.VatMossDetails.Details {
				msgs = append(msgs, vatMossErrors(d)...)
			}
		}
		if inv.VatMossDetail != nil {
			msgs = append(msgs, vatMossErrors(*inv.VatMossDetail)...)
		}
	}
	// Last resort before giving up: sweep the raw response for any error object, wherever
	// wFirma chose to hang it. The typed nodes above are the ones we know about — this is
	// what keeps the next unknown node from degrading to "unknown error" again.
	if len(msgs) == 0 {
		msgs = extractRawErrors(raw)
	}
	if len(msgs) == 0 && resp.Status.Message != "" {
		return resp.Status.Message
	}
	if len(msgs) == 0 {
		return "unknown error"
	}
	return strings.Join(msgs, "; ")
}

// vatMossErrors renders the validation errors of one OSS evidence entry.
func vatMossErrors(d VatMossDetailResp) []string {
	var msgs []string
	for _, ew := range d.Errors {
		msgs = append(msgs, fmt.Sprintf("vat_moss_detail[%s]: %s: %s",
			d.Type, ew.Error.Field, ew.Error.Message))
	}
	return msgs
}

// extractRawErrors walks a raw response and reports every {"error": {...}} object it
// contains, labelled with its JSON path. It is the untyped fallback for extractInvoiceErrors:
// wFirma attaches validation errors to nodes that are not always the ones it was given
// (OSS errors, for instance, come back on a `vat_moss_detail` sibling of the `vat_moss_details`
// that was submitted), and a missing node used to render as "unknown error" — a diagnosis
// that reads like the API said nothing when in fact it said exactly what was wrong.
func extractRawErrors(raw []byte) []string {
	var root interface{}
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil
	}

	var msgs []string
	var walk func(path string, node interface{})
	walk = func(path string, node interface{}) {
		switch n := node.(type) {
		case map[string]interface{}:
			if detail, ok := n["error"].(map[string]interface{}); ok {
				field, _ := detail["field"].(string)
				message, _ := detail["message"].(string)
				if message != "" {
					msgs = append(msgs, fmt.Sprintf("%s %s: %s", path, field, message))
					return
				}
			}
			for _, key := range sortedKeys(n) {
				walk(joinPath(path, key), n[key])
			}
		case []interface{}:
			for i, v := range n {
				walk(fmt.Sprintf("%s[%d]", path, i), v)
			}
		}
	}
	walk("", root)
	return msgs
}

// sortedKeys returns a map's keys in order, so a walk over a decoded JSON object
// produces the same message order on every run.
func sortedKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// joinPath appends a segment to a JSON path, dropping the noise every path in an
// invoice response shares ("invoices.0.invoice.").
func joinPath(path, key string) string {
	if path == "" {
		return key
	}
	joined := path + "." + key
	return strings.TrimPrefix(joined, "invoices.0.invoice.")
}

// stockErrorPhrases are the wFirma error message fragments that indicate a
// warehouse stock problem for a line item. Matching items are retried without
// their Good reference, which turns the line into a plain free-text product
// (name only, no code) and bypasses stock/batch tracking.
//   - "Stan magazynowy" — stock level cannot be negative
//   - "dostępnych partiach" — not enough quantity in available batches
var stockErrorPhrases = []string{"Stan magazynowy", "dostępnych partiach"}

// extractStockErrorIndices returns the integer indices of invoice content items
// that have a stock-related error (see stockErrorPhrases).
// These items should be retried without their Good reference to bypass stock tracking.
func extractStockErrorIndices(resp *InvoiceResponse) []int {
	var indices []int
	for _, wrapper := range resp.Invoices {
		for idxStr, cw := range wrapper.Invoice.InvoiceContents {
			for _, ew := range cw.InvoiceContent.Errors {
				if isStockError(ew.Error.Message) {
					idx, err := strconv.Atoi(idxStr)
					if err == nil {
						indices = append(indices, idx)
					}
				}
			}
		}
	}
	return indices
}

// isStockError reports whether a wFirma error message indicates a warehouse
// stock problem that can be worked around by dropping the Good reference.
func isStockError(msg string) bool {
	for _, p := range stockErrorPhrases {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

// ossSaleTypeGoods is the wFirma OSS "rodzaj sprzedaży" code for intra-EU distance
// selling of goods (WSTO — wewnątrzwspólnotowa sprzedaż towarów na odległość), which
// is what we ship.
//
// The field is a closed enum; wFirma rejects anything else with "Nieprawidłowy kod typu
// usługi. Dopuszczalne wartości to WO, SA, SB, SC, SD, SE, TA, TB, TC, TD, TE, TF, TG,
// TH, TJ, TK, BA, BB, INNE." Spelling it "WSTO" is what failed order 17104 for a full day.
//
// "WO" is the goods code. The letter pairs are the legacy MOSS taxonomy and classify
// *services* only (SA–SE electronic, TA–TK telecom, BA/BB broadcasting) — "BA" in
// particular is radio/TV broadcasting, and using it made wFirma label the invoice's OSS
// tab "BA - programy radiowe lub telewizyjne…", which is wrong for shipped goods.
const ossSaleTypeGoods = "WO"

// buildVatMossDetails constructs the OSS evidence wrapper for an invoice.
// Uses the customer's address as evidence type A and the delivery country as evidence type F.
func buildVatMossDetails(client *entity.ClientDetails, countryCode string) *VatMossDetailWrapper {
	var addrParts []string
	if client.Street != "" {
		addrParts = append(addrParts, client.Street)
	}
	if client.ZipCode != "" {
		addrParts = append(addrParts, client.ZipCode)
	}
	if client.City != "" {
		addrParts = append(addrParts, client.City)
	}
	if client.Country != "" {
		addrParts = append(addrParts, client.Country)
	}
	evidence1Desc := strings.Join(addrParts, ", ")
	if evidence1Desc == "" {
		evidence1Desc = countryCode
	}

	return &VatMossDetailWrapper{
		Detail: &VatMossDetail{
			Type:                 ossSaleTypeGoods,
			Evidence1Type:        "A", // billing/shipping address
			Evidence1Description: evidence1Desc,
			Evidence2Type:        "F", // other commercially relevant info
			Evidence2Description: "Order delivery address: " + countryCode,
		},
	}
}

// addPayment registers a payment against an existing invoice in wFirma (payments/add).
// Currently disabled — see the commented-out call in invoice().
func (c *Client) addPayment(ctx context.Context, invoice Invoice) error {
	paymentData := map[string]interface{}{
		"api": map[string]interface{}{
			"payments": []map[string]interface{}{
				{
					"payment": map[string]interface{}{
						"object_name": "invoice",
						"object_id":   invoice.Id,
						"value":       invoice.Total,
						"date":        invoice.Date,
					},
				},
			},
		},
	}

	payRes, err := c.request(ctx, "payments", "add", paymentData)
	if err != nil {
		return err
	}

	var payResp struct {
		Payments struct {
			Element0 struct {
				Payment struct {
					ID string `json:"id"`
				} `json:"payment"`
			} `json:"0"`
		} `json:"payments"`
		Status struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"status"`
	}
	if err = json.Unmarshal(payRes, &payResp); err != nil {
		return err
	}
	if payResp.Status.Code == "ERROR" {
		return errors.New(payResp.Status.Message)
	}
	return nil
}

// DownloadInvoice fetches the PDF file for a given wFirma invoice ID.
// Uses the invoices/download/{id} endpoint. Returns the saved filename and file metadata.
// InvoiceExists reports whether an invoice with the given wFirma ID still exists.
//
// It distinguishes three outcomes so callers can safely decide whether to re-create:
//   - (true, nil)   the invoice is present
//   - (false, nil)  the invoice is confirmed absent (deleted / never existed)
//   - (false, err)  the state could not be determined (network, auth, unexpected error)
//
// Callers must treat the error case as "unknown" and abort rather than risk creating
// a duplicate, while (false, nil) is the green light to re-create.
func (c *Client) InvoiceExists(ctx context.Context, invoiceID string) (bool, error) {
	if !c.enabled {
		return false, fmt.Errorf("wFirma is disabled")
	}
	if invoiceID == "" {
		return false, nil
	}

	// wFirma addresses a single object as invoices/get/{id}.
	res, err := c.request(ctx, "invoices", "get/"+invoiceID, map[string]interface{}{})
	if err != nil {
		return false, err
	}

	var resp InvoiceResponse
	if err := json.Unmarshal(res, &resp); err != nil {
		return false, fmt.Errorf("parse get response: %w", err)
	}

	if resp.Status.Code == "OK" {
		for _, w := range resp.Invoices {
			if w.Invoice.Id != "" {
				return true, nil
			}
		}
		// OK with no invoice payload means the object is gone.
		return false, nil
	}

	// A deleted invoice returns status code "NOT FOUND" (verified against the live API);
	// anything else is indeterminate, so surface it and let the caller abort.
	if isNotFoundStatus(resp.Status.Code) {
		return false, nil
	}
	msg := resp.Status.Message
	if msg == "" {
		msg = resp.Status.Code
	}
	return false, fmt.Errorf("wfirma get invoice %s: %s", invoiceID, msg)
}

// isNotFoundStatus matches wFirma's not-found status code (e.g. "NOT FOUND",
// "OBJECT NOT FOUND") case-insensitively.
func isNotFoundStatus(code string) bool {
	return strings.Contains(strings.ToUpper(code), "NOT FOUND")
}

// isFakturaType reports whether a wFirma invoice type counts as a faktura for
// duplicate-prevention purposes: a normal VAT invoice, or a KSeF-draft fallback
// (normal_draft) which is a pending faktura awaiting manual acceptance. Proformas —
// which carry the same id_external as their order — are deliberately excluded.
func isFakturaType(t string) bool {
	return t == string(invoiceNormal) || t == string(invoiceNormalDraft)
}

// FindInvoiceByExternalId returns the wFirma id of an existing faktura whose id_external
// matches externalId, or "" when none exists yet. Every invoice created here stamps
// id_external from params.ExternalRef() (order id for OpenCart, order UID for B2B — see
// invoice(), IdExternal), so this is the order-level dedup key.
//
// It is the idempotency guard for flows that hold no stored invoice id to verify with
// InvoiceExists — POST /v1/wf/invoice, POST /v1/b2b/invoice, and the retry queue. The
// wFirma-side conditions filter narrows to id_external only; the type is checked in Go rather
// than in the query because (a) a proforma for the same order shares the id_external and must
// not count, and (b) the result set for a single order is tiny. A split order yields several
// faktura parts sharing the id_external — any one blocks re-creation, so the first is returned.
//
// Callers must treat a non-nil error as "state unknown" and abort rather than risk a
// duplicate, mirroring InvoiceExists.
func (c *Client) FindInvoiceByExternalId(ctx context.Context, externalId string) (string, error) {
	existing, err := c.findFakturasByExternalId(ctx, externalId)
	if err != nil {
		return "", err
	}
	if len(existing) == 0 {
		return "", nil
	}
	return existing[0].Id, nil
}

// existingFaktura is a faktura already registered for an order: enough to decide whether
// a part still needs creating (Id) and whether the documents look like the parts of this
// order or like duplicates (Total).
type existingFaktura struct {
	Id    string
	Total float64
}

// sameAmount compares two gross amounts at cent precision, the granularity wFirma
// itself rounds to.
func sameAmount(a, b float64) bool {
	return math.Round(a*100) == math.Round(b*100)
}

// findFakturasByExternalId returns every faktura carrying externalId in id_external,
// ordered by id — i.e. in creation order, which for a split order is part order. An empty
// slice means the order has no faktura yet.
//
// Returning all of them (rather than just the first) is what lets invoice() resume a split
// order that failed part-way: n existing documents mean chunks 1..n are done and creation
// picks up at n+1. Proformas share the id_external and are filtered out here.
func (c *Client) findFakturasByExternalId(ctx context.Context, externalId string) ([]existingFaktura, error) {
	if !c.enabled {
		return nil, fmt.Errorf("wFirma is disabled")
	}
	if externalId == "" {
		return nil, nil
	}

	payload := map[string]interface{}{
		"api": map[string]interface{}{
			"invoices": map[string]interface{}{
				"parameters": map[string]interface{}{
					"limit": 100,
					"conditions": map[string]interface{}{
						"and": []map[string]interface{}{
							{
								"condition": map[string]interface{}{
									"field":    "id_external",
									"operator": "eq",
									"value":    externalId,
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
		return nil, fmt.Errorf("find invoice by external id %s: %w", externalId, err)
	}

	var resp InvoiceFindResponse
	if err := json.Unmarshal(res, &resp); err != nil {
		return nil, fmt.Errorf("parse find response: %w", err)
	}
	if resp.Status.Code == "ERROR" {
		msg := resp.Status.Message
		if msg == "" {
			msg = resp.Status.Code
		}
		return nil, fmt.Errorf("wfirma find invoice by external id %s: %s", externalId, msg)
	}

	// The invoices map also carries a non-invoice "parameters" entry (Id == ""), skipped here.
	var found []existingFaktura
	for _, w := range resp.Invoices {
		if w.Invoice.Id == "" || !isFakturaType(w.Invoice.Type) {
			continue
		}
		ex := existingFaktura{Id: w.Invoice.Id}
		// Total comes back as a formatted decimal string; an unparsable one leaves the
		// amount at zero, which only costs the caller its cross-check.
		if t, parseErr := strconv.ParseFloat(w.Invoice.Total, 64); parseErr == nil {
			ex.Total = t
		}
		found = append(found, ex)
	}
	// The response is a map, so iteration order is random; sort numerically to restore
	// creation order (wFirma ids are ascending integers).
	sort.Slice(found, func(i, j int) bool {
		a, errA := strconv.ParseInt(found[i].Id, 10, 64)
		b, errB := strconv.ParseInt(found[j].Id, 10, 64)
		if errA != nil || errB != nil {
			return found[i].Id < found[j].Id
		}
		return a < b
	})
	return found, nil
}

// DeleteProforma removes a proforma document from wFirma via invoices/delete/{id}.
//
// Deletion is irreversible, so it is guarded: the target is fetched first and the call
// is refused unless its type is exactly "proforma". This guarantees a normal VAT invoice
// can never be deleted through this path, even if a caller passes the wrong id. A document
// that is already absent (or confirmed gone) is treated as a successful no-op so callers
// can safely regenerate.
func (c *Client) DeleteProforma(ctx context.Context, invoiceID string) error {
	if !c.enabled {
		return fmt.Errorf("wFirma is disabled")
	}
	if invoiceID == "" {
		return nil
	}

	getRes, err := c.request(ctx, "invoices", "get/"+invoiceID, map[string]interface{}{})
	if err != nil {
		return fmt.Errorf("get invoice before delete: %w", err)
	}

	var getResp InvoiceResponse
	if err := json.Unmarshal(getRes, &getResp); err != nil {
		return fmt.Errorf("parse get response: %w", err)
	}

	if isNotFoundStatus(getResp.Status.Code) {
		return nil // already gone, nothing to delete
	}
	if getResp.Status.Code != "OK" {
		msg := getResp.Status.Message
		if msg == "" {
			msg = getResp.Status.Code
		}
		return fmt.Errorf("wfirma get invoice %s: %s", invoiceID, msg)
	}

	// The invoices map also carries a non-invoice "parameters" entry (Id == ""), skipped here.
	var found *InvoiceData
	for _, w := range getResp.Invoices {
		if w.Invoice.Id != "" {
			inv := w.Invoice
			found = &inv
			break
		}
	}
	if found == nil {
		return nil // OK with no payload means the object is gone
	}
	if found.Type != string(invoiceProforma) {
		return fmt.Errorf("refusing to delete invoice %s: type %q is not proforma", invoiceID, found.Type)
	}

	delRes, err := c.request(ctx, "invoices", "delete/"+invoiceID, map[string]interface{}{})
	if err != nil {
		return fmt.Errorf("delete invoice: %w", err)
	}

	var delResp InvoiceResponse
	if err := json.Unmarshal(delRes, &delResp); err != nil {
		return fmt.Errorf("parse delete response: %w", err)
	}
	if delResp.Status.Code != "OK" && !isNotFoundStatus(delResp.Status.Code) {
		msg := delResp.Status.Message
		if msg == "" {
			msg = delResp.Status.Code
		}
		return fmt.Errorf("wfirma delete invoice %s: %s", invoiceID, msg)
	}

	return nil
}

func (c *Client) DownloadInvoice(ctx context.Context, invoiceID string) (fileName string, meta *entity.FileMeta, err error) {
	if !c.enabled {
		return "", nil, fmt.Errorf("wFirma is disabled")
	}
	log := c.log.With(slog.String("invoice_id", invoiceID))
	defer func() {
		if r := recover(); r != nil {
			log.Error("panic recovered in DownloadInvoice", slog.Any("panic", r))
			fileName = ""
			meta = nil
			err = fmt.Errorf("panic in DownloadInvoice: %v", r)
		}
	}()

	// Wait for KSeF to finish processing before downloading. Until the invoice has an
	// assigned KSeF number, wFirma can only render an interim "transaction confirmation"
	// (a QR-only summary without line items), not the full invoice. See waitForKSefProcessed
	// and docs/wfirma-ksef-download-confirmation.md.
	c.waitForKSefProcessed(ctx, invoiceID, log)

	payload := map[string]interface{}{
		"api": map[string]interface{}{
			"invoices": map[string]interface{}{
				"parameters": []map[string]interface{}{
					{
						"parameter": map[string]interface{}{
							"name":  "page",
							"value": "invoice",
						},
					},
				},
			},
		},
	}

	q := url.Values{}
	q.Set("inputFormat", "json")
	endpoint := fmt.Sprintf("%s/invoices/download/%s?%s", c.baseURL, invoiceID, q.Encode())

	data, err := json.Marshal(payload)
	if err != nil {
		log.Error("marshal payload", sl.Err(err))
		return "", nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		log.Error("create request", sl.Err(err))
		return "", nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("appKey", c.appID)
	req.Header.Set("accessKey", c.accessKey)
	req.Header.Set("secretKey", c.secretKey)

	resp, err := c.hc.Do(req)
	if err != nil {
		log.Error("request failed", sl.Err(err))
		return "", nil, err
	}

	if resp.StatusCode >= 300 {
		_ = resp.Body.Close()
		log.Error("wfirma api", slog.String("status", resp.Status))
		return "", nil, fmt.Errorf("wfirma status: %s", resp.Status)
	}
	meta = &entity.FileMeta{
		ContentType:   resp.Header.Get("Content-Type"),
		ContentLength: resp.ContentLength,
	}

	ext := ".pdf"
	if !strings.Contains(meta.ContentType, "pdf") {
		return "", nil, fmt.Errorf("unsupported content type: %s", meta.ContentType)
	}
	fileName = uuid.New().String() + ext
	filePath := filepath.Join(c.filePath, fileName)

	f, err := os.Create(filePath)
	if err != nil {
		_ = resp.Body.Close()
		return "", nil, fmt.Errorf("create file: %w", err)
	}

	_, copyErr := io.Copy(f, resp.Body)
	_ = resp.Body.Close()

	// Sync to ensure data is flushed to disk before closing
	if copyErr == nil {
		copyErr = f.Sync()
	}

	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(filePath)
		return "", nil, fmt.Errorf("save file: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(filePath)
		return "", nil, fmt.Errorf("close file: %w", closeErr)
	}

	log.With(
		slog.String("file", fileName),
		slog.String("content_type", meta.ContentType),
		slog.Int64("content_length", meta.ContentLength),
	).Info("invoice downloaded")

	return fileName, meta, nil
}

// ksefDownloadPollInterval is how often waitForKSefProcessed re-checks the KSeF state.
const ksefDownloadPollInterval = 3 * time.Second

// waitForKSefProcessed blocks until the invoice has been processed by KSeF (a KSeF number
// is assigned) or the configured budget (c.ksefDownloadWait) elapses. It exists because a
// KSeF-submitted invoice that is still processing can only be downloaded as an interim
// "transaction confirmation" (a QR-only summary) rather than the full invoice PDF.
//
// It is deliberately fail-open and best-effort: any ambiguity (non-KSeF invoice, unreadable
// state, budget exhausted, or the gate disabled) results in proceeding with the download, so
// this can never do worse than the legacy immediate-download behavior. It only ever delays a
// download that we can positively tell is still pending in KSeF.
func (c *Client) waitForKSefProcessed(ctx context.Context, invoiceID string, log *slog.Logger) {
	if c.ksefDownloadWait <= 0 || invoiceID == "" {
		return
	}

	deadline := time.Now().Add(c.ksefDownloadWait)
	waited := false
	for {
		ready, pending := c.ksefReadiness(ctx, invoiceID)
		if ready || !pending {
			// ready: processed; not-pending: not a KSeF invoice or state unknown → download now.
			if waited {
				log.Debug("KSeF processing complete; proceeding with download")
			}
			return
		}
		if !waited {
			log.Debug("invoice not yet processed by KSeF; waiting before download")
			waited = true
		}
		if !time.Now().Add(ksefDownloadPollInterval).Before(deadline) {
			log.Warn("KSeF still processing after wait budget; downloading best-effort (may be a transaction confirmation)")
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(ksefDownloadPollInterval):
		}
	}
}

// ksefReadiness inspects invoices/get/{id} and reports whether the invoice is ready to
// download as a full invoice.
//
//   - ready:   the invoice carries an assigned KSeF number (or a success status), so the
//     full invoice PDF is available.
//   - pending: the invoice is a KSeF document still being processed (has KSeF state but no
//     number yet) — downloading now would yield the interim transaction confirmation.
//
// When the invoice has no KSeF fields at all (non-KSeF account/document) or the state cannot
// be read, it returns ready=false, pending=false so the caller downloads immediately.
//
// Field names are matched loosely (any key containing "ksef") to stay robust to wFirma's
// exact response shape: the KSeF number lives in a key like ksef_reference_number /
// ksef_number, the status in ksef_status, and the processing time in ksef_registration_date.
func (c *Client) ksefReadiness(ctx context.Context, invoiceID string) (ready, pending bool) {
	res, err := c.request(ctx, "invoices", "get/"+invoiceID, map[string]interface{}{})
	if err != nil {
		return false, false
	}

	var probe struct {
		Invoices map[string]struct {
			Invoice map[string]json.RawMessage `json:"invoice"`
		} `json:"invoices"`
		Status Status `json:"status"`
	}
	if err := json.Unmarshal(res, &probe); err != nil || probe.Status.Code != "OK" {
		return false, false
	}

	// Locate the single invoice object (the "invoices" map also carries a "parameters" entry).
	var fields map[string]json.RawMessage
	for _, w := range probe.Invoices {
		if len(w.Invoice) > 0 {
			fields = w.Invoice
			break
		}
	}
	if fields == nil {
		return false, false
	}
	return classifyKSefFields(fields)
}

// classifyKSefFields inspects an invoice object's fields and reports its KSeF readiness.
// It is the pure core of ksefReadiness (extracted for testing). Field names are matched
// loosely (any key containing "ksef") to stay robust to wFirma's exact response shape.
func classifyKSefFields(fields map[string]json.RawMessage) (ready, pending bool) {
	hasKSef := false
	numberAssigned := false
	statusOK := false
	for key, raw := range fields {
		lk := strings.ToLower(key)
		if !strings.Contains(lk, "ksef") {
			continue
		}
		hasKSef = true
		val := strings.TrimSpace(rawJSONString(raw))
		if val == "" {
			continue
		}
		switch {
		case strings.Contains(lk, "status"):
			// wFirma reports "ok" once the invoice is accepted by KSeF.
			if strings.EqualFold(val, "ok") || strings.EqualFold(val, "accepted") {
				statusOK = true
			}
		case strings.Contains(lk, "number"), strings.Contains(lk, "numer"),
			strings.Contains(lk, "reference"), strings.Contains(lk, "registration"):
			// The KSeF number (ksef_reference_number) and registration date
			// (ksef_registration_date) are only populated once KSeF accepts the invoice.
			// Deliberately NOT matching a bare "date" — a submission/send date can be set
			// while the invoice is still pending and must not read as processed.
			numberAssigned = true
		}
	}

	if !hasKSef {
		return false, false // non-KSeF invoice — nothing to wait for
	}
	if numberAssigned || statusOK {
		return true, false
	}
	return false, true // KSeF invoice, not processed yet
}

// rawJSONString renders a JSON value as a trimmed string, tolerating both string and
// non-string (number) encodings — wFirma is inconsistent about quoting scalar fields.
func rawJSONString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	t := strings.TrimSpace(string(raw))
	if t == "null" {
		return ""
	}
	return strings.Trim(t, `"`)
}
