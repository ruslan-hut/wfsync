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
	// For domestic (PL) invoices, use numeric strings like "23", "8", "0".
	vatWDT  = "WDT"  // 0% intra-community goods delivery (EU buyer with VAT number)
	vatEXP  = "EXP"  // 0% export of goods (non-EU buyer)
	vatNP   = "NP"   // not subject to Polish VAT (non-EU services)
	vatNPUE = "NPUE" // not subject to Polish VAT, EU reverse charge (EU services)
	vatZW   = "ZW"   // exempt from VAT

	// shippingVatCode overrides the VAT code for shipping line items.
	// When empty, shipping uses the same VAT code as goods.
	// Set to a specific code (e.g. "NP", "ZW", "23") to tax shipping differently.
	shippingVatCode = ""

	// shippingSku is the default SKU used for shipping line items when no SKU is set.
	// Used to look up the wFirma good ID for shipping costs.
	shippingSku = "Zwrot"
)

// euCountries contains EU member state codes (ISO 3166-1 alpha-2), excluding Poland.
// Used to determine whether a foreign contractor qualifies for intra-community (WDT) rates.
var euCountries = map[string]bool{
	"AT": true, "BE": true, "BG": true, "HR": true, "CY": true,
	"CZ": true, "DK": true, "EE": true, "FI": true, "FR": true,
	"DE": true, "GR": true, "HU": true, "IE": true, "IT": true,
	"LV": true, "LT": true, "LU": true, "MT": true, "NL": true,
	"PT": true, "RO": true, "SK": true, "SI": true, "ES": true,
	"SE": true,
}

type Database interface {
	SaveInvoice(id string, invoice interface{}) error
	SaveCheckoutParams(params *entity.CheckoutParams) error
	UpdateCheckoutParams(params *entity.CheckoutParams) error
	GetProductBySku(sku string) (*entity.Product, error)
	SaveProduct(product *entity.Product) error
}

type Client struct {
	enabled   bool
	hc        *http.Client
	db        Database
	baseURL   string
	accessKey string
	secretKey string
	appID     string
	filePath  string
	log       *slog.Logger
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
func (c *Client) resolveGoodId(ctx context.Context, sku string) *int64 {
	log := c.log.With(slog.String("sku", sku))

	// Try local DB first.
	if c.db != nil {
		product, err := c.db.GetProductBySku(sku)
		if err != nil {
			log.Warn("get product by sku", sl.Err(err))
		} else if product != nil && product.WfirmaId > 0 {
			return &product.WfirmaId
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
	return &goodId
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

// resolveGoodsVatCode determines the correct VAT code for goods based on Polish tax rules:
//   - PL or unknown country → numeric rate from the order (e.g. "23")
//   - EU country + VAT number → "WDT" (intra-community delivery, 0%)
//   - EU country without VAT number → standard domestic rate (buyer is a consumer)
//   - Non-EU country → "EXP" (export, 0%)
func resolveGoodsVatCode(taxRate int, countryCode string, hasTaxId bool) string {
	if countryCode == "" || countryCode == "PL" {
		return strconv.Itoa(taxRate)
	}
	if euCountries[countryCode] && hasTaxId {
		return vatWDT
	}
	if !euCountries[countryCode] {
		return vatEXP
	}
	return strconv.Itoa(taxRate)
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
	goodsVat := resolveGoodsVatCode(params.TaxRate(), countryCode, hasTaxId)

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
			Vat:   vatCode,
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

	total := float64(params.Total) / 100.0
	issueDate := params.Created.Format("2006-01-02")
	paymentDate := params.Created.AddDate(0, 0, defaultPaymentDays).Format("2006-01-02")

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
		slog.String("tax", params.TaxTitle),
		slog.String("email", params.ClientDetails.Email),
		slog.String("name", params.ClientDetails.Name),
		slog.String("country", params.ClientDetails.Country),
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
