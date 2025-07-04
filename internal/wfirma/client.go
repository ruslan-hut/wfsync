package wfirma

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
	"wfsync/entity"
	"wfsync/lib/sl"
)

type Database interface {
	SaveInvoice(id string, invoice interface{}) error
	SaveCheckoutParams(params *entity.CheckoutParams) error
	UpdateCheckoutParams(params *entity.CheckoutParams) error
}

type Client struct {
	hc        *http.Client
	db        Database
	baseURL   string
	accessKey string
	secretKey string
	appID     string
	log       *slog.Logger
}

type Config struct {
	AccessKey string
	SecretKey string
	AppID     string
}

func NewClient(cfg Config, db Database, logger *slog.Logger) *Client {
	return &Client{
		hc:        &http.Client{Timeout: 10 * time.Second},
		db:        db,
		baseURL:   "https://api2.wfirma.pl",
		accessKey: cfg.AccessKey,
		secretKey: cfg.SecretKey,
		appID:     cfg.AppID,
		log:       logger.With(sl.Module("wfirma")),
	}
}

// request sends a signed POST to wFirma API using Access/Secret key headers.
func (c *Client) request(ctx context.Context, module, action string, payload interface{}) ([]byte, error) {
	log := c.log.With(
		slog.String("module", module),
		slog.String("action", action),
	)

	var err error
	status := "ERROR"
	t1 := time.Now()
	defer func() {
		t2 := time.Now()
		log.Debug("wFirma API request completed",
			slog.String("duration", fmt.Sprintf("%.3fms", float64(t2.Sub(t1))/float64(time.Millisecond))),
			slog.String("status", status))
	}()

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

	status = resp.Status
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

	// If not found, create a new contractor.
	payload := map[string]interface{}{
		"api": map[string]interface{}{
			"contractors": []map[string]interface{}{
				{
					"contractor": map[string]interface{}{
						"name":        customer.Name,
						"email":       customer.Email,
						"country":     customer.Country,
						"zip":         customer.ZipCode,
						"city":        customer.City,
						"street":      customer.Street,
						"tax_id_type": "other",
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
	var addResp struct {
		Contractors struct {
			Element0 struct {
				Contractor struct {
					ID string `json:"id"`
				} `json:"contractor"`
			} `json:"0"`
		} `json:"contractors"`
	}
	if err = json.Unmarshal(createRes, &addResp); err != nil {
		c.log.Error("parse contractor creation response", sl.Err(err))
		return "", err
	}
	if addResp.Contractors.Element0.Contractor.ID == "" {
		c.log.Error("no contractor ID returned from wFirma", slog.String("email", customer.Email))
		return "", fmt.Errorf("no contractor id returned")
	}
	contractorID := addResp.Contractors.Element0.Contractor.ID
	c.log.Info("new contractor created",
		slog.String("email", customer.Email),
		slog.String("name", customer.Name),
		slog.String("contractorID", contractorID))
	return contractorID, nil
}

func (c *Client) getContractor(ctx context.Context, email string) (string, error) {
	if email == "" {
		return "", nil
	}
	c.log.Debug("looking up contractor by email", slog.String("email", email))

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
			c.log.Info("found existing contractor",
				slog.String("email", email),
				slog.String("contractor_id", contractorID))
			return contractorID, nil
		}
	} else {
		c.log.Warn("searching for contractor",
			slog.String("email", email),
			slog.String("error", err.Error()))
	}

	return "", nil
}

func (c *Client) DownloadInvoice(ctx context.Context, invoiceID string) (io.ReadCloser, *entity.FileMeta, error) {
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
		return nil, nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		log.Error("create request", sl.Err(err))
		return nil, nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("appKey", c.appID)
	req.Header.Set("accessKey", c.accessKey)
	req.Header.Set("secretKey", c.secretKey)

	resp, err := c.hc.Do(req)
	if err != nil {
		log.Error("request failed", sl.Err(err))
		return nil, nil, err
	}

	if resp.StatusCode >= 300 {
		resp.Body.Close()
		log.Error("wFirma API returned error", slog.String("status", resp.Status))
		return nil, nil, fmt.Errorf("wfirma status: %s", resp.Status)
	}
	meta := &entity.FileMeta{
		ContentType:   resp.Header.Get("Content-Type"),
		ContentLength: resp.ContentLength,
	}
	log.With(
		slog.String("content_type", meta.ContentType),
		slog.Int64("content_length", meta.ContentLength),
	).Debug("download invoice response")

	return resp.Body, meta, nil
}

func (c *Client) RegisterInvoice(ctx context.Context, params *entity.CheckoutParams) error {
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

	if params.ClientDetails == nil {
		return fmt.Errorf("client details not provided")
	}
	if len(params.LineItems) == 0 {
		return fmt.Errorf("no line items provided")
	}

	contractorID, err := c.getContractor(ctx, params.ClientDetails.Email)
	if err != nil {
		return fmt.Errorf("contractor: %w", err)
	}
	if contractorID == "" {
		log.Debug("no contractor found")
		email := params.ClientDetails.Email
		if email == "" {
			email = fmt.Sprintf("%s@example.com", uuid.New().String())
		}
		contractorID, err = c.createContractor(ctx, params.ClientDetails)
		if err != nil {
			return fmt.Errorf("create contractor: %w", err)
		}
	}
	log = log.With(slog.String("contractor_id", contractorID))

	contractor := &Contractor{
		Id: contractorID,
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
		Type:        "normal",
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
		return fmt.Errorf("add invoice: %w", err)
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
		return err
	}

	invID := addResp.Invoices.Element0.Invoice.ID
	if invID == "" {
		c.log.Error("no invoice ID returned from wFirma")
		return fmt.Errorf("no invoice id returned")
	}

	invoice.Id = invID
	if c.db != nil {
		err = c.db.SaveInvoice(invID, invoice)
		if err != nil {
			c.log.Error("save invoice",
				sl.Err(err))
		}
		params.InvoiceId = invID
		err = c.db.UpdateCheckoutParams(params)
		if err != nil {
			c.log.Error("update checkout params",
				sl.Err(err))
		}
	}

	log.Info("invoice created successfully",
		slog.String("wfirma_id", invID))

	if !params.Paid {
		return nil
	}

	payment := map[string]interface{}{
		"api": map[string]interface{}{
			"payments": []map[string]interface{}{
				{
					"payment": map[string]interface{}{
						"object_name": "invoice",
						"object_id":   invID,
						"value":       total,
						"date":        params.Created.Format("2006-01-02"),
					},
				},
			},
		},
	}

	payRes, err := c.request(ctx, "payments", "add", payment)
	if err != nil {
		log.Error("add payment",
			sl.Err(err))
		return fmt.Errorf("add payment: %w", err)
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
		log.Error("parse payment creation response",
			sl.Err(err))
		return err
	}
	if payResp.Status.Code == "ERROR" {
		log.Error("add payment response",
			slog.String("error", payResp.Status.Message))
		//return fmt.Errorf("add payment failed")
	}

	return nil
}
