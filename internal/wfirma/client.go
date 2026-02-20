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
	"sync"
	"time"
	"wfsync/entity"
	"wfsync/internal/config"
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

	// VAT codes for cross-border transactions (passed in invoicecontent "vat" field).
	// wfirma accepts any numeric VAT rate (e.g. "23", "21", "19", "8", "0"),
	// which is needed for OSS-registered companies invoicing with destination-country rates.
	vatWDT  = "WDT"  // 0% intra-community goods delivery (EU buyer with VAT number)
	vatEXP  = "EXP"  // 0% export of goods (non-EU buyer)
	vatNP   = "NP"   // not subject to Polish VAT (non-EU services)
	vatNPUE = "NPUE" // not subject to Polish VAT, EU reverse charge (EU services)
	vatZW   = "ZW"   // exempt from VAT

	// typeOfSaleSW is the wFirma type_of_sale value for distance selling of goods
	// under the EU OSS (One-Stop Shop) scheme. Required when invoicing EU B2C
	// customers with a destination-country VAT rate.
	typeOfSaleSW = "SW"

	// shippingVatCode overrides the VAT code for shipping line items.
	// When empty, shipping uses the same VAT code as goods.
	// Set to a specific code (e.g. "NP", "ZW", "23") to tax shipping differently.
	shippingVatCode = ""

	// shippingSku is the default SKU used for shipping line items when no SKU is set.
	// Used to look up the wFirma good ID for shipping costs.
	shippingSku = "Zwrot"
)

// polishVatCodes contains VAT code strings accepted by the wFirma "vat" field.
// Any rate not in this set (e.g. "25" for Denmark, "21" for Netherlands) must be sent
// via the "vat_code" object reference with the numeric ID from vat_codes/findAll.
var polishVatCodes = map[string]bool{
	"23": true, "22": true, "8": true, "7": true, "5": true, "3": true, "0": true,
	vatWDT: true, vatEXP: true, vatNP: true, vatNPUE: true, vatZW: true,
}

// euCountries contains EU member state codes (ISO 3166-1 alpha-2), excluding Poland.
// Used to determine whether a foreign contractor qualifies for intra-community (WDT) rates.
// b2bCustomerGroups contains OpenCart customer group IDs that represent B2B customers.
// B2B customers with a TaxID in the EU get WDT (0%), without TaxID get 23% Polish rate.
// B2C customers always get the destination-country rate regardless of TaxID.
var b2bCustomerGroups = map[int]bool{
	6: true, 7: true, 16: true, 18: true, 19: true,
}

var euCountries = map[string]bool{
	"AT": true, "BE": true, "BG": true, "HR": true, "CY": true,
	"CZ": true, "DK": true, "EE": true, "FI": true, "FR": true,
	"DE": true, "GR": true, "HU": true, "IE": true, "IT": true,
	"LV": true, "LT": true, "LU": true, "MT": true, "NL": true,
	"PT": true, "RO": true, "SK": true, "SI": true, "ES": true,
	"SE": true,
}

// VATProvider supplies dynamic EU country membership and standard VAT rates.
// When nil or unverified, the client falls back to the hardcoded euCountries map.
type VATProvider interface {
	IsEUCountry(code string) bool
	GetStandardRate(code string) float64
	Verified() bool
}

// VIESProvider validates EU VAT numbers against the VIES service.
// When set, the client logs a warning if a B2B customer's TaxId fails validation.
// Validation is non-blocking: the result is logged but does not affect invoice creation.
type VIESProvider interface {
	ValidateTaxId(taxId string) bool
}

type Database interface {
	SaveInvoice(id string, invoice interface{}) error
	SaveCheckoutParams(params *entity.CheckoutParams) error
	UpdateCheckoutParams(params *entity.CheckoutParams) error
	GetProductBySku(sku string) (*entity.Product, error)
	SaveProduct(product *entity.Product) error
	GetInvoicesByDateRange(from, to, invType string) ([]*entity.LocalInvoice, error)
	DeleteInvoiceById(id string) error
	UpdateInvoiceNumber(id, number string) error
}

type Client struct {
	enabled   bool
	hc        *http.Client
	db        Database
	vatRates  VATProvider
	vies      VIESProvider
	baseURL   string
	accessKey string
	secretKey string
	appID     string
	filePath  string
	log       *slog.Logger

	vatCodesMu sync.RWMutex
	vatCodes   map[string]int64 // vat code string → wFirma vat_code entity ID
	vatCodesAt time.Time        // when vatCodes was last refreshed
}

type Config struct {
	AccessKey string
	SecretKey string
	AppID     string
}

func NewClient(conf *config.Config, logger *slog.Logger) *Client {
	return &Client{
		enabled:   conf.WFirma.Enabled,
		hc:        &http.Client{Timeout: 20 * time.Second},
		baseURL:   "https://api2.wfirma.pl",
		accessKey: conf.WFirma.AccessKey,
		secretKey: conf.WFirma.SecretKey,
		appID:     conf.WFirma.AppID,
		filePath:  conf.FilePath,
		log:       logger.With(sl.Module("wfirma")),
	}
}

func (c *Client) SetDatabase(db Database) {
	c.db = db
}

// SetVATProvider injects a dynamic EU VAT rate provider.
// When set, resolveGoodsVatCode uses it instead of the hardcoded euCountries map.
func (c *Client) SetVATProvider(vp VATProvider) {
	c.vatRates = vp
}

// SetVIESProvider injects a VIES VAT number validator.
// When set, invoice creation logs a warning if a B2B TaxId fails VIES validation.
func (c *Client) SetVIESProvider(vp VIESProvider) {
	c.vies = vp
}

// request sends a signed POST to the wFirma API (https://api2.wfirma.pl).
// All endpoints use POST with JSON input/output.
// Auth is via HTTP headers: appKey, accessKey, secretKey.
func (c *Client) request(ctx context.Context, module, action string, payload interface{}) ([]byte, error) {
	log := c.log.With(
		slog.String("module", module),
		slog.String("action", action),
	)

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	q := url.Values{}
	q.Set("inputFormat", "json")
	q.Set("outputFormat", "json")
	endpoint := fmt.Sprintf("%s/%s/%s?%s", c.baseURL, module, action, q.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		log.Error("create request", slog.String("error", err.Error()))
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("appKey", c.appID)
	req.Header.Set("accessKey", c.accessKey)
	req.Header.Set("secretKey", c.secretKey)

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 300 {
		log.Error("wFirma API returned error",
			slog.String("status", resp.Status),
			slog.String("body", string(body)))
		return nil, fmt.Errorf("wfirma %s: %s", resp.Status, body)
	}

	return body, nil
}

// createContractor registers a new contractor in wFirma and returns its ID.
// Contractor fields: name, email, country (ISO 3166 alpha-2), zip, city, street, nip, tax_id_type.
// tax_id_type: "other" = no tax ID provided, "custom" = tax ID present.
func (c *Client) createContractor(ctx context.Context, customer *entity.ClientDetails) (string, error) {
	if customer == nil {
		return "", fmt.Errorf("no customer")
	}
	if customer.Name == "" {
		customer.Name = "Kontrahent " + customer.Email
	}
	if customer.ZipCode == "" {
		customer.ZipCode = "01-001"
	}
	if customer.City == "" {
		customer.City = "Warszawa"
	}
	taxIdType := "other"
	if customer.TaxId != "" {
		taxIdType = "custom"
	}

	countryCode := customer.CountryCode()
	if countryCode == "PL" {
		customer.ZipCode = customer.NormalizeZipCode()
	}

	// If not found, create a new contractor.
	payload := map[string]interface{}{
		"api": map[string]interface{}{
			"contractors": []map[string]interface{}{
				{
					"contractor": map[string]interface{}{
						"name":        customer.Name,
						"email":       customer.Email,
						"country":     countryCode,
						"zip":         customer.ZipCode,
						"city":        customer.City,
						"street":      customer.Street,
						"tax_id_type": taxIdType,
						"nip":         customer.TaxId,
					},
				},
			},
		},
	}
	createRes, err := c.request(ctx, "contractors", "add", payload)
	if err != nil {
		c.log.Error("create contractor",
			slog.String("email", customer.Email),
			sl.Err(err))
		return "", err
	}
	var addResp Response
	if err = json.Unmarshal(createRes, &addResp); err != nil {
		c.log.Error("parse contractor creation response", sl.Err(err))
		return "", err
	}
	contr := addResp.Contractors["0"].Contractor
	if addResp.Status.Code == "ERROR" {
		if len(contr.ErrorsRaw) > 0 {
			for _, w := range contr.ErrorsRaw { // берём первый элемент мапы
				c.log.With(
					slog.String("field", w.Error.Field),
					slog.String("message", w.Error.Message),
					slog.String("method", w.Error.Method.Name),
					slog.String("parameters", w.Error.Method.Parameters),
					slog.String("email", customer.Email),
					slog.String("name", customer.Name),
					slog.String("tg_topic", entity.TopicError),
				).Error("add contractor")
				break
			}
		}
		return "", fmt.Errorf("no contractor id returned")
	}
	if contr.ID == "" {
		c.log.Error("no contractor ID returned from wFirma", slog.Any("error", createRes))
		return "", fmt.Errorf("no contractor id returned")
	}
	c.log.Debug("new contractor created",
		slog.String("email", customer.Email),
		slog.String("name", customer.Name),
		slog.String("contractorID", contr.ID))
	return contr.ID, nil
}

// getContractor searches for an existing contractor by email. Returns empty string if not found.
func (c *Client) getContractor(ctx context.Context, email string) (string, error) {
	if email == "" {
		return "", nil
	}
	log := c.log.With(slog.String("email", email))

	// Try to find by customer email first.
	search := map[string]interface{}{
		"api": map[string]interface{}{
			"contractors": map[string]interface{}{
				"parameters": map[string]interface{}{
					"conditions": []map[string]interface{}{
						{
							"condition": map[string]interface{}{
								"field":    "email",
								"operator": "eq",
								"value":    email,
							},
						},
					},
				},
			},
		},
	}

	res, err := c.request(ctx, "contractors", "find", search)
	if err == nil {
		var findResp struct {
			Contractors struct {
				Element0 struct {
					Contractor struct {
						ID string `json:"id"`
					} `json:"contractor"`
				} `json:"0"`
			} `json:"contractors"`
		}
		_ = json.Unmarshal(res, &findResp)
		if findResp.Contractors.Element0.Contractor.ID != "" {
			contractorID := findResp.Contractors.Element0.Contractor.ID
			log.Debug("found existing contractor",
				slog.String("contractor_id", contractorID))
			return contractorID, nil
		}
	} else {
		log.Warn("searching for contractor", sl.Err(err))
	}

	return "", nil
}

// findGoodBySku searches the wFirma goods catalog by code (SKU).
// Returns the good ID and name if found, 0/"" if not found or on error.
func (c *Client) findGoodBySku(ctx context.Context, sku string) (int64, string, error) {
	search := map[string]interface{}{
		"api": map[string]interface{}{
			"goods": map[string]interface{}{
				"parameters": map[string]interface{}{
					"conditions": []map[string]interface{}{
						{
							"condition": map[string]interface{}{
								"field":    "code",
								"operator": "eq",
								"value":    sku,
							},
						},
					},
				},
			},
		},
	}

	res, err := c.request(ctx, "goods", "find", search)
	if err != nil {
		return 0, "", err
	}

	var findResp struct {
		Goods struct {
			Element0 struct {
				Good struct {
					ID   json.Number `json:"id"`
					Name string      `json:"name"`
				} `json:"good"`
			} `json:"0"`
		} `json:"goods"`
	}
	if err = json.Unmarshal(res, &findResp); err != nil {
		return 0, "", err
	}
	idStr := findResp.Goods.Element0.Good.ID.String()
	if idStr == "" || idStr == "0" {
		return 0, "", nil
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return 0, "", fmt.Errorf("parse good id %q: %w", idStr, err)
	}
	return id, findResp.Goods.Element0.Good.Name, nil
}

// resolveGoodId looks up the wFirma good ID for a SKU: first from local DB, then from the wFirma API.
// If found via API, the mapping is saved to local DB for future lookups.
// Returns nil if not found or on any error (non-fatal).
func (c *Client) resolveGoodId(ctx context.Context, sku string) *GoodRef {
	log := c.log.With(slog.String("sku", sku))

	// Try local DB first.
	if c.db != nil {
		product, err := c.db.GetProductBySku(sku)
		if err != nil {
			log.Warn("get product by sku", sl.Err(err))
		} else if product != nil && product.WfirmaId > 0 {
			return &GoodRef{ID: product.WfirmaId}
		}
	}

	// Fall back to wFirma API.
	goodId, goodName, err := c.findGoodBySku(ctx, sku)
	if err != nil {
		log.Warn("find good by sku", sl.Err(err))
		return nil
	}
	if goodId == 0 {
		log.Info("no good was found by sku")
		return nil
	}

	// Cache the mapping locally.
	if c.db != nil {
		err = c.db.SaveProduct(&entity.Product{
			Sku:      sku,
			WfirmaId: goodId,
			Name:     goodName,
		})
		if err != nil {
			log.Warn("save product", sl.Err(err))
		}
	}
	return &GoodRef{ID: goodId}
}

// fetchVatCodes retrieves all VAT codes from the wFirma API (vat_codes/find)
// and returns a map of code string → entity ID.
// This is needed because non-Polish VAT rates (e.g. "25" for DK) require the
// vat_code object reference — the plain "vat" string field only accepts Polish codes.
func (c *Client) fetchVatCodes(ctx context.Context) (map[string]int64, error) {
	const pageSize = 100
	result := make(map[string]int64)

	for page := 0; ; page++ {
		payload := map[string]interface{}{
			"api": map[string]interface{}{
				"vat_codes": map[string]interface{}{
					"parameters": map[string]interface{}{
						"limit": pageSize,
						"page":  page,
					},
				},
			},
		}

		res, err := c.request(ctx, "vat_codes", "find", payload)
		if err != nil {
			return nil, fmt.Errorf("fetch vat codes page %d: %w", page, err)
		}

		var resp VatCodesResponse
		if err = json.Unmarshal(res, &resp); err != nil {
			return nil, fmt.Errorf("parse vat codes response: %w", err)
		}
		if resp.Status.Code == "ERROR" {
			return nil, fmt.Errorf("vat_codes API error")
		}

		for _, wrapper := range resp.VatCodes {
			vc := wrapper.VatCode
			if vc.ID == "" || vc.Code == "" {
				continue
			}
			id, err := strconv.ParseInt(vc.ID, 10, 64)
			if err != nil {
				continue
			}
			result[vc.Code] = id
		}

		fetched := (page + 1) * pageSize
		if fetched >= resp.Parameters.Total || len(resp.VatCodes) == 0 {
			break
		}
	}

	c.log.Info("vat codes loaded", slog.Int("count", len(result)))
	return result, nil
}

// vatCodesTTL is how long fetched VAT codes stay valid before a refresh is attempted.
const vatCodesTTL = 24 * time.Hour

// getVatCodeId looks up the wFirma vat_code entity ID for a given code string.
// Thread-safe. Lazily fetches on first call and refreshes after vatCodesTTL.
// Keeps stale cache on refresh failure. Returns 0 if the code is not found.
func (c *Client) getVatCodeId(ctx context.Context, code string) int64 {
	c.vatCodesMu.RLock()
	id, ok := c.vatCodes[code]
	stale := c.vatCodes == nil || time.Since(c.vatCodesAt) > vatCodesTTL
	c.vatCodesMu.RUnlock()

	if !stale && ok {
		return id
	}
	if !stale {
		return 0
	}

	// Cache is empty or expired — refresh under write lock.
	c.vatCodesMu.Lock()
	defer c.vatCodesMu.Unlock()

	// Double-check after acquiring write lock (another goroutine may have refreshed).
	if c.vatCodes != nil && time.Since(c.vatCodesAt) <= vatCodesTTL {
		return c.vatCodes[code]
	}

	codes, err := c.fetchVatCodes(ctx)
	if err != nil {
		c.log.Warn("fetch vat codes failed, using stale cache", sl.Err(err))
		// Keep stale cache — return whatever we have.
		return c.vatCodes[code]
	}
	c.vatCodes = codes
	c.vatCodesAt = time.Now()
	return c.vatCodes[code]
}

// setContentVat sets the VAT on a Content line item.
// For standard Polish codes (23, 8, 5, etc.) it uses the "vat" string field.
// For non-Polish rates (EU OSS) it looks up the vat_code entity ID and uses the "vat_code" reference.
// Falls back to the "vat" string field if the vat_code lookup fails.
func (c *Client) setContentVat(ctx context.Context, content *Content, vatCode string) {
	if polishVatCodes[vatCode] {
		content.Vat = vatCode
		return
	}
	// Non-Polish rate — resolve via vat_code reference.
	if id := c.getVatCodeId(ctx, vatCode); id > 0 {
		content.VatCode = &VatCodeRef{ID: id}
		return
	}
	// Fallback: send as vat string (may be ignored by the API).
	c.log.Warn("vat_code not found, falling back to vat string",
		slog.String("code", vatCode))
	content.Vat = vatCode
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
		resp.Body.Close()
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
		resp.Body.Close()
		return "", nil, fmt.Errorf("create file: %w", err)
	}

	_, copyErr := io.Copy(f, resp.Body)
	resp.Body.Close()

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

// resolveGoodsVatCode determines the correct VAT code for invoice line items.
// The company is registered under the EU OSS (One-Stop Shop) scheme, so the site
// calculates the destination-country VAT rate and we pass it through to wfirma.
//
// B2B rules (OpenCart customer groups 6, 7, 16, 18, 19):
//   - PL or unknown country → numeric rate from the order (e.g. "23")
//   - EU country + VAT number → "WDT" (intra-community delivery, 0%)
//   - EU country without VAT number → "23" (Polish rate, not destination rate)
//   - Non-EU country → "EXP" (export, 0%)
//
// B2C rules (all other customer groups):
//   - PL or unknown country → numeric rate from the order (e.g. "23")
//   - EU country → destination-country rate (e.g. "21" for NL, "19" for DE), TaxID irrelevant
//   - Non-EU country → "EXP" (export, 0%)
func resolveGoodsVatCode(taxRate int, countryCode string, hasTaxId bool, b2b bool, vp VATProvider) string {
	if countryCode == "" || countryCode == "PL" {
		return strconv.Itoa(taxRate)
	}

	isEU := false
	if vp != nil {
		isEU = vp.IsEUCountry(countryCode)
	} else {
		isEU = euCountries[countryCode]
	}

	if !isEU {
		return vatEXP
	}
	// EU country — branch on B2B vs B2C
	if b2b {
		if hasTaxId {
			return vatWDT
		}
		return "23"
	}
	// B2C: destination-country rate, TaxID irrelevant
	return strconv.Itoa(taxRate)
}

// formatCustomerGroup returns a human-readable label like "3 (B2C)" or "6 (B2B)".
func formatCustomerGroup(group int) string {
	if b2bCustomerGroups[group] {
		return fmt.Sprintf("%d (B2B)", group)
	}
	return fmt.Sprintf("%d (B2C)", group)
}

// invoice builds and sends an invoices/add request to the wFirma API.
// Flow: validate params → find/create contractor → build invoice with contents → POST to API → persist result.
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
			log.Info("VIES validation passed",
				slog.String("tax_id", params.ClientDetails.TaxId),
				slog.String("country", countryCode))
		} else {
			log.Warn("VIES validation failed",
				slog.String("tax_id", params.ClientDetails.TaxId),
				slog.String("country", countryCode),
				slog.String("email", params.ClientDetails.Email),
				slog.String("tg_topic", entity.TopicError))
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
		c.setContentVat(ctx, content, vatCode)
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

	total := float64(params.Total) / 100.0
	issueDate := params.Created.Format("2006-01-02")
	paymentDate := params.Created.AddDate(0, 0, defaultPaymentDays).Format("2006-01-02")

	// Determine if this is an EU OSS sale (B2C to another EU country).
	// wFirma requires type_of_sale to accept destination-country VAT rates.
	isEU := false
	if vp != nil {
		isEU = vp.IsEUCountry(countryCode)
	} else {
		isEU = euCountries[countryCode]
	}
	var typeOfSale string
	if !isB2B && isEU && countryCode != "" && countryCode != "PL" {
		typeOfSale = typeOfSaleSW
	}

	invoice := &Invoice{
		Contractor:    contractor,
		Type:          string(invType),
		PriceType:     "brutto",
		PaymentMethod: defaultPaymentMethod,
		PaymentDate:   paymentDate,
		DisposalDate:  issueDate, // date of sale defaults to the issue date
		Total:         total,
		IdExternal:    params.OrderId,
		Description:   "Numer zamówienia: " + params.OrderId,
		Date:          issueDate,
		Currency:      strings.ToUpper(params.Currency),
		TypeOfSale:    typeOfSale,
		Contents:      contents,
	}

	addPayload := map[string]interface{}{
		"api": map[string]interface{}{
			"invoices": []map[string]interface{}{
				{
					"invoice": invoice,
				},
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
		log.With(
			slog.Any("response", addRes),
		).Debug("invoice creation response")
		return nil, err
	}
	var resultInvoice InvoiceData
	if wrapper, ok := addResp.Invoices["0"]; ok {
		resultInvoice = wrapper.Invoice
	}
	if errWrap, ok := resultInvoice.Errors["0"]; ok {
		log.With(
			slog.String("error", errWrap.Error.Message),
			slog.String("field", errWrap.Error.Field),
			slog.String("method", errWrap.Error.Method.Name),
			slog.String("parameters", errWrap.Error.Method.Parameters),
			slog.String("tg_topic", entity.TopicError),
		).Warn("invoice creation")
		return nil, fmt.Errorf("invoice creation error: %s", errWrap.Error.Message)
	}
	if resultInvoice.Id == "" {
		return nil, fmt.Errorf("no invoice id returned from wFirma")
	}

	//log.With(
	//	slog.Any("invoice", addRes),
	//).Debug("invoice created")

	invoice.Id = resultInvoice.Id
	invoice.Number = resultInvoice.Number
	if c.db != nil {
		err = c.db.SaveInvoice(invoice.Id, invoice)
		if err != nil {
			log.Error("save invoice",
				sl.Err(err))
		}
		if invType == invoiceProforma {
			params.ProformaId = invoice.Id
		} else {
			params.InvoiceId = invoice.Id
		}
		err = c.db.UpdateCheckoutParams(params)
		if err != nil {
			log.Error("update checkout params",
				sl.Err(err))
		}
	}

	payment := &entity.Payment{
		Amount:  int64(invoice.Total * 100),
		Id:      invoice.Id,
		OrderId: params.OrderId,
	}

	c.log.With(
		slog.String("wfirma_id", invoice.Id),
		slog.String("wfirma_number", resultInvoice.Number),
		slog.String("order_id", params.OrderId),
		slog.String("total", fmt.Sprintf("%.2f", total)),
		slog.String("tax", fmt.Sprintf("%s %s", goodsVat, typeOfSale)),
		slog.String("email", params.ClientDetails.Email),
		slog.String("name", params.ClientDetails.Name),
		slog.String("country", params.ClientDetails.Country),
		slog.String("customer_group", formatCustomerGroup(params.CustomerGroup)),
		slog.String("currency", params.Currency),
		slog.String("tg_topic", entity.TopicInvoice),
	).Info("invoice created")

	// *** payment creation is disabled ***
	//if params.Paid {
	//	err = c.addPayment(ctx, *invoice)
	//	if err != nil {
	//		log.Error("add payment",
	//			slog.String("wfirma_id", invoice.Id),
	//			sl.Err(err))
	//	}
	//}

	return payment, nil
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

// findInvoices fetches all invoices from wFirma matching a date range and type.
// Paginates through results with 100 items per page.
func (c *Client) findInvoices(ctx context.Context, from, to string, invType invoiceType) ([]InvoiceData, error) {
	const pageSize = 100
	var all []InvoiceData

	for page := 0; ; page++ {
		payload := map[string]interface{}{
			"api": map[string]interface{}{
				"invoices": map[string]interface{}{
					"parameters": map[string]interface{}{
						"limit": pageSize,
						"page":  page,
						"conditions": map[string]interface{}{
							"and": []map[string]interface{}{
								{
									"condition": map[string]interface{}{
										"field":    "date",
										"operator": "ge",
										"value":    from,
									},
								},
								{
									"condition": map[string]interface{}{
										"field":    "date",
										"operator": "le",
										"value":    to,
									},
								},
								{
									"condition": map[string]interface{}{
										"field":    "type",
										"operator": "eq",
										"value":    string(invType),
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
			return nil, fmt.Errorf("find invoices page %d: %w", page, err)
		}

		var findResp InvoiceFindResponse
		if err = json.Unmarshal(res, &findResp); err != nil {
			return nil, fmt.Errorf("parse find response: %w", err)
		}
		if findResp.Status.Code == "ERROR" {
			return nil, fmt.Errorf("find invoices: API error")
		}

		for _, wrapper := range findResp.Invoices {
			all = append(all, wrapper.Invoice)
		}

		// Stop if we've fetched all results
		fetched := (page + 1) * pageSize
		if fetched >= findResp.Parameters.Total || len(findResp.Invoices) == 0 {
			break
		}
	}

	return all, nil
}

// SyncFromRemote pulls invoices from wFirma for the given date range and syncs them to local DB.
// Flow: fetch remote normal invoices, upsert each locally (with number), delete local records
// whose IDs are absent from the remote set.
func (c *Client) SyncFromRemote(ctx context.Context, from, to string) (*entity.SyncResult, error) {
	if !c.enabled {
		return nil, fmt.Errorf("wFirma is disabled")
	}
	if c.db == nil {
		return nil, fmt.Errorf("database not connected")
	}
	log := c.log.With(slog.String("op", "sync_from_remote"), slog.String("from", from), slog.String("to", to))

	// Fetch remote invoices
	remoteInvoices, err := c.findInvoices(ctx, from, to, invoiceNormal)
	if err != nil {
		return nil, fmt.Errorf("find remote invoices: %w", err)
	}

	result := &entity.SyncResult{
		RemoteCount: len(remoteInvoices),
	}

	// Build a set of remote IDs and upsert each invoice locally
	remoteIDs := make(map[string]bool, len(remoteInvoices))
	for _, inv := range remoteInvoices {
		remoteIDs[inv.Id] = true
		// Build an Invoice struct to save via existing SaveInvoice (upsert)
		localInv := &Invoice{
			Id:     inv.Id,
			Number: inv.Number,
			Type:   inv.Type,
			Date:   inv.Date,
		}
		if err = c.db.SaveInvoice(inv.Id, localInv); err != nil {
			log.Warn("upsert invoice", slog.String("id", inv.Id), sl.Err(err))
			continue
		}
		result.Upserted++
	}

	// Get local invoices for the same range
	localInvoices, err := c.db.GetInvoicesByDateRange(from, to, string(invoiceNormal))
	if err != nil {
		return nil, fmt.Errorf("get local invoices: %w", err)
	}
	result.LocalCount = len(localInvoices)

	// Delete local records absent from remote
	for _, local := range localInvoices {
		if !remoteIDs[local.Id] {
			if err = c.db.DeleteInvoiceById(local.Id); err != nil {
				log.Warn("delete orphaned invoice", slog.String("id", local.Id), sl.Err(err))
				continue
			}
			result.Deleted++
		}
	}

	log.With(
		slog.Int("remote", result.RemoteCount),
		slog.Int("local", result.LocalCount),
		slog.Int("upserted", result.Upserted),
		slog.Int("deleted", result.Deleted),
	).Info("sync from remote completed")

	return result, nil
}

// SyncToRemote pushes locally stored invoices to wFirma for the given date range.
// Flow: read local invoices, fetch remote invoices for the same range, find local IDs
// absent from remote, re-create each via invoices/add, replace old local record with new ID/number.
func (c *Client) SyncToRemote(ctx context.Context, from, to string) (*entity.SyncResult, error) {
	if !c.enabled {
		return nil, fmt.Errorf("wFirma is disabled")
	}
	if c.db == nil {
		return nil, fmt.Errorf("database not connected")
	}
	log := c.log.With(slog.String("op", "sync_to_remote"), slog.String("from", from), slog.String("to", to))

	// Get local invoices
	localInvoices, err := c.db.GetInvoicesByDateRange(from, to, string(invoiceNormal))
	if err != nil {
		return nil, fmt.Errorf("get local invoices: %w", err)
	}

	// Fetch remote invoices for the same range
	remoteInvoices, err := c.findInvoices(ctx, from, to, invoiceNormal)
	if err != nil {
		return nil, fmt.Errorf("find remote invoices: %w", err)
	}

	result := &entity.SyncResult{
		LocalCount:  len(localInvoices),
		RemoteCount: len(remoteInvoices),
	}

	// Build a set of remote IDs
	remoteIDs := make(map[string]bool, len(remoteInvoices))
	for _, inv := range remoteInvoices {
		remoteIDs[inv.Id] = true
	}

	// Re-create local invoices that are missing on remote
	for _, local := range localInvoices {
		if remoteIDs[local.Id] {
			continue
		}

		newId, newNumber, err := c.recreateInvoice(ctx, local)
		if err != nil {
			log.Warn("recreate invoice",
				slog.String("old_id", local.Id),
				sl.Err(err))
			continue
		}

		// Delete old local record
		if err = c.db.DeleteInvoiceById(local.Id); err != nil {
			log.Warn("delete old local invoice", slog.String("id", local.Id), sl.Err(err))
		}

		// Save new record with updated ID and number
		local.Id = newId
		local.Number = newNumber
		if err = c.db.SaveInvoice(newId, local); err != nil {
			log.Warn("save recreated invoice", slog.String("id", newId), sl.Err(err))
		}

		result.Recreated++
		log.Info("invoice recreated",
			slog.String("old_id", local.Id),
			slog.String("new_id", newId),
			slog.String("new_number", newNumber))
	}

	log.With(
		slog.Int("local", result.LocalCount),
		slog.Int("remote", result.RemoteCount),
		slog.Int("recreated", result.Recreated),
	).Info("sync to remote completed")

	return result, nil
}

// recreateInvoice posts an invoices/add request to wFirma using stored LocalInvoice data.
// Returns the new invoice ID and number assigned by the API.
func (c *Client) recreateInvoice(ctx context.Context, local *entity.LocalInvoice) (string, string, error) {
	// Build contractor reference
	var contractor *Contractor
	if local.Contractor != nil {
		contractor = &Contractor{ID: local.Contractor.ID}
	}

	// Build content lines
	var contents []*ContentLine
	for _, line := range local.Contents {
		if line.Content == nil {
			continue
		}
		content := &Content{
			Name:  line.Content.Name,
			Count: line.Content.Count,
			Price: line.Content.Price,
			Unit:  line.Content.Unit,
		}
		if line.Content.Good != nil {
			content.Good = &GoodRef{ID: line.Content.Good.ID}
		}
		// Preserve vat_code reference from stored data; for old records that only
		// have a vat string, resolve it through setContentVat.
		if line.Content.VatCode != nil && line.Content.VatCode.ID > 0 {
			content.VatCode = &VatCodeRef{ID: line.Content.VatCode.ID}
		} else {
			c.setContentVat(ctx, content, line.Content.Vat)
		}
		contents = append(contents, &ContentLine{Content: content})
	}

	invoice := &Invoice{
		Contractor:    contractor,
		Type:          local.Type,
		PriceType:     local.PriceType,
		PaymentMethod: local.PaymentMethod,
		PaymentDate:   local.PaymentDate,
		DisposalDate:  local.DisposalDate,
		Total:         local.Total,
		IdExternal:    local.IdExternal,
		Description:   local.Description,
		Date:          local.Date,
		Currency:      local.Currency,
		Contents:      contents,
	}

	payload := map[string]interface{}{
		"api": map[string]interface{}{
			"invoices": []map[string]interface{}{
				{
					"invoice": invoice,
				},
			},
		},
	}

	res, err := c.request(ctx, "invoices", "add", payload)
	if err != nil {
		return "", "", fmt.Errorf("add invoice: %w", err)
	}

	var addResp InvoiceResponse
	if err = json.Unmarshal(res, &addResp); err != nil {
		return "", "", fmt.Errorf("parse add response: %w", err)
	}

	var resultInvoice InvoiceData
	if wrapper, ok := addResp.Invoices["0"]; ok {
		resultInvoice = wrapper.Invoice
	}
	if resultInvoice.Id == "" {
		return "", "", fmt.Errorf("no invoice id returned from wFirma")
	}

	return resultInvoice.Id, resultInvoice.Number, nil
}
