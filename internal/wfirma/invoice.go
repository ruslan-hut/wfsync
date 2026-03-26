package wfirma

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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
func (c *Client) invoice(ctx context.Context, invType invoiceType, params *entity.CheckoutParams) (*entity.Payment, error) {
	log := c.log.With(slog.String("session_id", params.SessionId), slog.String("order_id", params.OrderId))
	defer func() {
		if r := recover(); r != nil {
			log.Error("panic recovered in RegisterInvoice", slog.Any("panic", r))
		}
	}()
	if c.db != nil {
		err := c.db.SaveCheckoutParams(params)
		if err != nil {
			log.Error("save checkout params", sl.Err(err))
		}
	}

	err := params.Validate()
	if err != nil {
		return nil, fmt.Errorf("invalid checkout params: %w", err)
	}

	contractorID, err := c.getContractor(ctx, params.ClientDetails.Email)
	if err != nil {
		return nil, fmt.Errorf("contractor: %w", err)
	}
	if contractorID == "" {
		email := params.ClientDetails.Email
		if email == "" {
			email = fmt.Sprintf("%s@example.com", uuid.New().String())
		}
		contractorID, err = c.createContractor(ctx, params.ClientDetails)
		if err != nil {
			return nil, fmt.Errorf("create contractor: %w", err)
		}
	} else if params.ClientDetails.TaxId != "" {
		// Existing contractor — ensure wFirma has the current tax ID.
		// Without this, WDT invoices fail when the contractor was previously created without a NIP.
		if err := c.updateContractor(ctx, contractorID, params.ClientDetails); err != nil {
			log.Warn("update contractor tax id", sl.Err(err))
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
		if c.vies.ValidateTaxId(params.ClientDetails.TaxId) {
			log.Debug("VIES validation passed",
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
	// Internal rate (from vatlookup.eu) always takes priority over OpenCart data.
	if vp != nil && !isB2B && countryCode != "" && countryCode != "PL" {
		if internalRate := vp.GetStandardRate(countryCode); internalRate > 0 && internalRate != float64(opencartRate) {
			log.Warn("VAT rate mismatch: opencart rate differs from internal rate, using internal",
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

	for partIdx, chunk := range chunks {
		partNum := partIdx + 1

		// Calculate the total for this chunk from its line items.
		var chunkTotal float64
		for _, cl := range chunk {
			chunkTotal += cl.Content.Price * float64(cl.Content.Count)
		}

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
			IdExternal:    params.OrderId,
			Description:   description,
			Date:          issueDate,
			Currency:      strings.ToUpper(params.Currency),
			Contents:      chunk,
		}

		if isOSS {
			inv.VatMossDetails = buildVatMossDetails(params.ClientDetails, countryCode)
		}

		resultInv, err := c.submitInvoice(ctx, log, inv, chunk)
		if err != nil {
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
			slog.String("tax", goodsVat),
			slog.Bool("oss", isOSS),
			slog.String("email", params.ClientDetails.Email),
			slog.String("name", params.ClientDetails.Name),
			slog.String("country", params.ClientDetails.Country),
			slog.String("tax_id", params.ClientDetails.TaxId),
			slog.String("customer_group", formatCustomerGroup(params.CustomerGroup)),
			slog.String("currency", params.Currency),
			slog.String("part", fmt.Sprintf("%d/%d", partNum, totalParts)),
			slog.String("tg_topic", entity.TopicInvoice),
		).Info("invoice created")

		if firstPayment == nil {
			firstPayment = &entity.Payment{
				Amount:  int64(chunkTotal * 100),
				Id:      inv.Id,
				OrderId: params.OrderId,
			}
		}
	}

	// Persist the first invoice ID back to checkout params.
	if c.db != nil && firstPayment != nil {
		if invType == invoiceProforma {
			params.ProformaId = firstPayment.Id
		} else {
			params.InvoiceId = firstPayment.Id
		}
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

	if addResp.Status.Code == "ERROR" {
		errMsg := extractInvoiceErrors(&addResp)

		stockErrIdxs := extractStockErrorIndices(&addResp)
		if len(stockErrIdxs) > 0 {
			log.With(
				slog.String("error", errMsg),
				slog.String("tg_topic", entity.TopicError),
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
				retryErrMsg := extractInvoiceErrors(&addResp)
				log.With(
					slog.String("error", retryErrMsg),
					slog.String("response", truncateBody(string(addRes))),
					slog.String("tg_topic", entity.TopicError),
				).Warn("retry invoice creation error")
				return nil, fmt.Errorf("wFirma error (retry): %s", retryErrMsg)
			}
		} else {
			log.With(
				slog.String("error", errMsg),
				slog.String("response", truncateBody(string(addRes))),
				slog.String("tg_topic", entity.TopicError),
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

// chunkContents splits a slice of content lines into chunks of at most size elements.
// If the total number of items is below softLimit, no split is performed.
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

// truncateBody shortens a response body for logging. If the body exceeds 500 chars,
// only the first 100 and last 100 chars are kept.
func truncateBody(s string) string {
	if len(s) <= 500 {
		return s
	}
	return s[:100] + " ... [truncated] ... " + s[len(s)-100:]
}

// extractInvoiceErrors collects all error messages from the invoice response,
// including contractor-level and invoicecontent-level validation errors.
func extractInvoiceErrors(resp *InvoiceResponse) string {
	var msgs []string
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
	}
	if len(msgs) == 0 && resp.Status.Message != "" {
		return resp.Status.Message
	}
	if len(msgs) == 0 {
		return "unknown error"
	}
	return strings.Join(msgs, "; ")
}

// extractStockErrorIndices returns the integer indices of invoice content items
// that have a stock-related error ("Stan magazynowy nie może być ujemny").
// These items should be retried without their Good reference to bypass stock tracking.
func extractStockErrorIndices(resp *InvoiceResponse) []int {
	var indices []int
	for _, wrapper := range resp.Invoices {
		for idxStr, cw := range wrapper.Invoice.InvoiceContents {
			for _, ew := range cw.InvoiceContent.Errors {
				if strings.Contains(ew.Error.Message, "Stan magazynowy") {
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
			Type:                 "BA", // goods (WSTO)
			Evidence1Type:        "A",  // billing/shipping address
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
		return fmt.Errorf(payResp.Status.Message)
	}
	return nil
}

// DownloadInvoice fetches the PDF file for a given wFirma invoice ID.
// Uses the invoices/download/{id} endpoint. Returns the saved filename and file metadata.
func (c *Client) DownloadInvoice(ctx context.Context, invoiceID string) (string, *entity.FileMeta, error) {
	if !c.enabled {
		return "", nil, fmt.Errorf("wFirma is disabled")
	}
	log := c.log.With(slog.String("invoice_id", invoiceID))
	defer func() {
		if r := recover(); r != nil {
			log.Error("panic recovered in DownloadInvoice", slog.Any("panic", r))
		}
	}()

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
	meta := &entity.FileMeta{
		ContentType:   resp.Header.Get("Content-Type"),
		ContentLength: resp.ContentLength,
	}

	ext := ".pdf"
	if !strings.Contains(meta.ContentType, "pdf") {
		return "", nil, fmt.Errorf("unsupported content type: %s", meta.ContentType)
	}
	fileName := uuid.New().String() + ext
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
