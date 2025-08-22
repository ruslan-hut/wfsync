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
	"strings"
	"time"
	"wfsync/entity"
	"wfsync/internal/config"
	"wfsync/lib/sl"

	"github.com/google/uuid"
)

type invoiceType string

const (
	invoiceProforma invoiceType = "proforma"
	invoiceNormal   invoiceType = "normal"
)

type Database interface {
	SaveInvoice(id string, invoice interface{}) error
	SaveCheckoutParams(params *entity.CheckoutParams) error
	UpdateCheckoutParams(params *entity.CheckoutParams) error
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
		hc:        &http.Client{Timeout: 10 * time.Second},
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

// request sends a signed POST to wFirma API using Access/Secret key headers.
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

// getOrCreateContractor returns contractor ID in wFirma for the invoice customer.
func (c *Client) createContractor(ctx context.Context, customer *entity.ClientDetails) (string, error) {
	if customer == nil {
		return "", fmt.Errorf("no customer")
	}
	if customer.Name == "" {
		customer.Name = "Kontrahent " + customer.Email
	}
	if customer.ZipCode == "" {
		customer.ZipCode = "01-249"
	}
	if customer.City == "" {
		customer.City = "Warszawa"
	}
	taxIdType := "other"
	if customer.TaxId != "" {
		taxIdType = "custom"
	}

	// If not found, create a new contractor.
	payload := map[string]interface{}{
		"api": map[string]interface{}{
			"contractors": []map[string]interface{}{
				{
					"contractor": map[string]interface{}{
						"name":        customer.Name,
						"email":       customer.Email,
						"country":     customer.CountryCode(),
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
		ext = ""
	}
	fileName := uuid.New().String() + ext
	filePath := filepath.Join(c.filePath, fileName)

	f, err := os.Create(filePath)
	if err != nil {
		resp.Body.Close()
		return "", nil, fmt.Errorf("create file: %w", err)
	}
	if _, err = io.Copy(f, resp.Body); err != nil {
		f.Close()
		resp.Body.Close()
		os.Remove(filePath)
		return "", nil, fmt.Errorf("save file: %w", err)
	}
	f.Close()
	resp.Body.Close()

	log.With(
		slog.String("file", fileName),
		slog.String("content_type", meta.ContentType),
		slog.Int64("content_length", meta.ContentLength),
	).Info("invoice downloaded")

	return fileName, meta, nil
}

func (c *Client) RegisterInvoice(ctx context.Context, params *entity.CheckoutParams) (*entity.Payment, error) {
	if !c.enabled {
		return nil, fmt.Errorf("wFirma is disabled")
	}
	return c.invoice(ctx, invoiceNormal, params)
}

func (c *Client) RegisterProforma(ctx context.Context, params *entity.CheckoutParams) (*entity.Payment, error) {
	if !c.enabled {
		return nil, fmt.Errorf("wFirma is disabled")
	}
	return c.invoice(ctx, invoiceProforma, params)
}

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

	var contents []*ContentLine
	for _, line := range params.LineItems {
		contents = append(contents, &ContentLine{
			Content: &Content{
				Name:  line.Name,
				Count: line.Qty,
				Price: float64(line.Price) / 100.0,
				Unit:  "szt.",
			},
		})
	}

	//iso := func(ts int64) string { return time.Unix(ts, 0).Format("2006-01-02") }
	total := float64(params.Total) / 100.0

	invoice := &Invoice{
		Contractor:  contractor,
		Type:        string(invType),
		PriceType:   "brutto",
		Total:       total,
		IdExternal:  params.OrderId,
		Description: "Numer zamówienia: " + params.OrderId,
		Date:        params.Created.Format("2006-01-02"),
		Currency:    strings.ToUpper(params.Currency),
		Contents:    contents,
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

	var addResp struct {
		Invoices struct {
			Element0 struct {
				Invoice struct {
					ID string `json:"id"`
				} `json:"invoice"`
			} `json:"0"`
		} `json:"invoices"`
	}
	if err = json.Unmarshal(addRes, &addResp); err != nil {
		log.Error("parse invoice creation response",
			sl.Err(err))
		return nil, err
	}
	log.With(
		slog.Any("response", addRes),
	).Debug("create invoice")

	invID := addResp.Invoices.Element0.Invoice.ID
	if invID == "" {
		log.Error("no invoice ID returned from wFirma")
		return nil, fmt.Errorf("no invoice id returned")
	}

	invoice.Id = invID
	if c.db != nil {
		err = c.db.SaveInvoice(invID, invoice)
		if err != nil {
			log.Error("save invoice",
				sl.Err(err))
		}
		if invType == invoiceProforma {
			params.ProformaId = invID
		} else {
			params.InvoiceId = invID
		}
		err = c.db.UpdateCheckoutParams(params)
		if err != nil {
			log.Error("update checkout params",
				sl.Err(err))
		}
	}

	payment := &entity.Payment{
		Amount:  int64(invoice.Total * 100),
		Id:      invID,
		OrderId: params.OrderId,
	}

	c.log.With(
		slog.String("wfirma_id", invID),
		slog.String("order_id", params.OrderId),
		slog.String("total", fmt.Sprintf("%.2f", total)),
		slog.String("email", params.ClientDetails.Email),
		slog.String("name", params.ClientDetails.Name),
		slog.String("country", params.ClientDetails.Country),
		slog.String("currency", params.Currency),
	).Info("invoice created")

	if params.Paid {
		err = c.addPayment(ctx, *invoice)
		if err != nil {
			log.Error("add payment",
				slog.String("wfirma_id", invID),
				sl.Err(err))
		}
	}

	return payment, nil
}

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
