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

	"github.com/stripe/stripe-go/v76"
)

type Client struct {
	hc        *http.Client
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

func NewClient(cfg Config, logger *slog.Logger) *Client {
	return &Client{
		hc:        &http.Client{Timeout: 10 * time.Second},
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
		log.Error("marshal payload", slog.String("error", err.Error()))
		return nil, err
	}

	q := url.Values{}
	q.Set("inputFormat", "json")
	q.Set("outputFormat", "json")
	endpoint := fmt.Sprintf("%s/%s/%s?%s", c.baseURL, module, action, q.Encode())
	log = log.With(slog.String("endpoint", endpoint))

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
		log.Error("request failed", slog.String("error", err.Error()))
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
func (c *Client) createContractor(ctx context.Context, customer *stripe.Customer, email string) (string, error) {
	name := ""
	country := ""
	zip := ""
	city := ""
	street := ""
	if customer != nil {
		name = customer.Name
		if customer.Address != nil {
			country = customer.Address.Country
			zip = customer.Address.PostalCode
			city = customer.Address.City
			street = customer.Address.Line1
		}
	}
	if name == "" {
		name = "Kontrahent " + email
	}
	if zip == "" {
		zip = "01-249"
	}
	if city == "" {
		city = "Warszawa"
	}

	// If not found, create a new contractor.
	payload := map[string]interface{}{
		"api": map[string]interface{}{
			"contractors": []map[string]interface{}{
				{
					"contractor": map[string]interface{}{
						"name":        name,
						"email":       email,
						"country":     country,
						"zip":         zip,
						"city":        city,
						"street":      street,
						"tax_id_type": "other",
					},
				},
			},
		},
	}
	createRes, err := c.request(ctx, "contractors", "add", payload)
	if err != nil {
		c.log.Error("create contractor",
			slog.String("email", email),
			slog.String("error", err.Error()))
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
		c.log.Error("parse contractor creation response", slog.String("error", err.Error()))
		return "", err
	}
	if addResp.Contractors.Element0.Contractor.ID == "" {
		c.log.Error("no contractor ID returned from wFirma", slog.String("email", email))
		return "", fmt.Errorf("no contractor id returned")
	}
	contractorID := addResp.Contractors.Element0.Contractor.ID
	c.log.Info("successfully created new contractor",
		slog.String("email", email),
		slog.String("name", name),
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

// SyncInvoice creates/updates invoice in wFirma and attaches PDF.
func (c *Client) SyncInvoice(ctx context.Context, inv *stripe.Invoice, _ []byte) error {
	log := c.log.With(slog.String("invoice_number", inv.Number), slog.String("customer_email", inv.CustomerEmail))
	defer func() {
		if r := recover(); r != nil {
			log.Error("panic recovered in SyncInvoice", slog.Any("panic", r))
		}
	}()

	contractorID, err := c.getContractor(ctx, inv.CustomerEmail)
	if err != nil {
		return fmt.Errorf("contractor: %w", err)
	}
	if contractorID == "" {
		email := inv.CustomerEmail
		if email == "" {
			email = fmt.Sprintf("%s@example.com", inv.Number)
		}
		contractorID, err = c.createContractor(ctx, inv.Customer, email)
		if err != nil {
			return fmt.Errorf("create contractor: %w", err)
		}
	}

	contractor := map[string]interface{}{
		"id": contractorID,
	}

	// Build contents from invoice lines.
	var contents []map[string]interface{}
	for _, line := range inv.Lines.Data {
		contents = append(contents, map[string]interface{}{
			"invoicecontent": map[string]interface{}{
				"name":  line.Description,
				"count": line.Quantity,
				"price": float64(line.Amount) / 100.0,
				"unit":  "szt.",
			},
		})
	}

	if inv.ShippingCost != nil && inv.ShippingCost.AmountTotal > 0 {
		contents = append(contents, map[string]interface{}{
			"invoicecontent": map[string]interface{}{
				"name":  "Zwrot kosztów transportu towarów",
				"count": 1,
				"price": float64(inv.ShippingCost.AmountTotal) / 100.0,
			},
		})
	}

	iso := func(ts int64) string { return time.Unix(ts, 0).Format("2006-01-02") }
	//attach := ""
	//if len(pdf) > 0 {
	//	attach = base64.StdEncoding.EncodeToString(pdf)
	//}

	total := float64(inv.Total) / 100.0
	orderId := inv.Number
	if inv.Metadata != nil {
		orderId = inv.Metadata["order_id"]
	}

	addPayload := map[string]interface{}{
		"api": map[string]interface{}{
			"invoices": []map[string]interface{}{
				{
					"invoice": map[string]interface{}{
						"contractor":      contractor,
						"type":            "normal",
						"price_type":      "brutto",
						"total":           total,
						"id_external":     inv.Number,
						"description":     orderId,
						"date":            iso(inv.PeriodStart),
						"currency":        strings.ToUpper(string(inv.Currency)),
						"invoicecontents": contents,
					},
				},
			},
		},
	}

	addRes, err := c.request(ctx, "invoices", "add", addPayload)
	if err != nil {
		log.Error("add invoice",
			slog.String("error", err.Error()))
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
			slog.String("error", err.Error()))
		return err
	}

	invID := addResp.Invoices.Element0.Invoice.ID
	if invID == "" {
		log.Error("no invoice ID returned from wFirma")
		return fmt.Errorf("no invoice id returned")
	}
	log.Info("invoice created successfully",
		slog.String("wfirma_id", invID))

	if !inv.Paid {
		return nil
	}

	payment := map[string]interface{}{
		"api": map[string]interface{}{
			"payments": []map[string]interface{}{
				{
					"payment": map[string]interface{}{
						"object_name": "invoice",
						"object_id":   invID,
						"value":       float64(inv.AmountPaid) / 100.0,
						"date":        iso(inv.PeriodStart),
					},
				},
			},
		},
	}

	payRes, err := c.request(ctx, "payments", "add", payment)
	if err != nil {
		log.Error("add payment",
			slog.String("error", err.Error()))
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
			slog.String("error", err.Error()))
		return err
	}
	if payResp.Status.Code == "ERROR" {
		log.Error("add payment",
			slog.String("error", payResp.Status.Message))
		//return fmt.Errorf("add payment failed")
	}

	return nil
}

func (c *Client) SyncSession(ctx context.Context, sess *stripe.CheckoutSession, lineItems []*stripe.LineItem) error {
	log := c.log.With(slog.String("session_id", sess.ID), slog.String("customer_email", sess.CustomerEmail))
	defer func() {
		if r := recover(); r != nil {
			log.Error("panic recovered in SyncSession", slog.Any("panic", r))
		}
	}()

	contractorID, err := c.getContractor(ctx, sess.CustomerEmail)
	if err != nil {
		return fmt.Errorf("contractor: %w", err)
	}
	if contractorID == "" {
		log.Debug("no contractor found")
		email := sess.CustomerEmail
		if email == "" {
			email = fmt.Sprintf("%s@example.com", uuid.New().String())
		}
		contractorID, err = c.createContractor(ctx, sess.Customer, email)
		if err != nil {
			return fmt.Errorf("create contractor: %w", err)
		}
	}
	log = log.With(slog.String("contractor_id", contractorID))

	contractor := map[string]interface{}{
		"id": contractorID,
	}

	var contents []map[string]interface{}
	for _, line := range lineItems {
		contents = append(contents, map[string]interface{}{
			"invoicecontent": map[string]interface{}{
				"name":  line.Description,
				"count": line.Quantity,
				"price": float64(line.AmountTotal) / 100.0,
				"unit":  "szt.",
			},
		})
	}

	iso := func(ts int64) string { return time.Unix(ts, 0).Format("2006-01-02") }
	total := float64(sess.AmountTotal) / 100.0
	orderId := sess.ID
	if sess.Metadata != nil {
		id, ok := sess.Metadata["order_id"]
		if ok {
			orderId = id
		}
	}

	addPayload := map[string]interface{}{
		"api": map[string]interface{}{
			"invoices": []map[string]interface{}{
				{
					"invoice": map[string]interface{}{
						"contractor":      contractor,
						"type":            "normal",
						"price_type":      "brutto",
						"total":           total,
						"id_external":     orderId,
						"description":     "ID: " + orderId,
						"date":            iso(sess.Created),
						"currency":        strings.ToUpper(string(sess.Currency)),
						"invoicecontents": contents,
					},
				},
			},
		},
	}

	addRes, err := c.request(ctx, "invoices", "add", addPayload)
	if err != nil {
		log.Error("add invoice",
			slog.String("error", err.Error()))
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
	log.Info("invoice created successfully",
		slog.String("wfirma_id", invID))

	if sess.PaymentStatus != stripe.CheckoutSessionPaymentStatusPaid {
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
						"date":        iso(sess.Created),
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
