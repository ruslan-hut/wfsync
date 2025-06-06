package wfirma

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
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
		log:       logger.With(slog.String("pkg", "wfirma")),
	}
}

// request sends a signed POST to wFirma API using Access/Secret key headers.
func (c *Client) request(ctx context.Context, module, action string, payload interface{}) ([]byte, error) {
	c.log.Debug("Preparing wFirma API request",
		slog.String("module", module),
		slog.String("action", action))

	data, err := json.Marshal(payload)
	if err != nil {
		c.log.Error("Failed to marshal payload", slog.String("error", err.Error()))
		return nil, err
	}

	q := url.Values{}
	q.Set("inputFormat", "json")
	q.Set("outputFormat", "json")
	endpoint := fmt.Sprintf("%s/%s/%s?%s", c.baseURL, module, action, q.Encode())
	c.log.Debug("Request endpoint prepared", slog.String("endpoint", endpoint))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		c.log.Error("Failed to create request", slog.String("error", err.Error()))
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("appKey", c.appID)
	req.Header.Set("accessKey", c.accessKey)
	req.Header.Set("secretKey", c.secretKey)

	c.log.Debug("Sending request to wFirma API")
	resp, err := c.hc.Do(req)
	if err != nil {
		c.log.Error("Request failed", slog.String("error", err.Error()))
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 300 {
		c.log.Error("wFirma API returned error",
			slog.String("status", resp.Status),
			slog.String("body", string(body)))
		return nil, fmt.Errorf("wfirma %s: %s", resp.Status, body)
	}

	c.log.Debug("Request completed successfully",
		slog.String("status", resp.Status),
		slog.Int("bodySize", len(body)))
	return body, nil
}

// getOrCreateContractor returns contractor ID in wFirma for the invoice customer.
func (c *Client) getOrCreateContractor(ctx context.Context, inv *stripe.Invoice) (int64, error) {
	email := inv.CustomerEmail
	if email == "" {
		email = fmt.Sprintf("%s@example.com", uuid.New().String()) // fallback to dummy email if not available
	}
	c.log.Debug("Looking up contractor by email", slog.String("email", email))

	// Try to find by customer email first.
	search := map[string]interface{}{
		"parameters": map[string]interface{}{
			"query": email,
		},
	}
	res, err := c.request(ctx, "contractors", "find", search)
	if err == nil {
		var findResp struct {
			Contractors []struct {
				ID int64 `json:"id"`
			} `json:"contractor"`
		}
		_ = json.Unmarshal(res, &findResp)
		if len(findResp.Contractors) > 0 {
			contractorID := findResp.Contractors[0].ID
			c.log.Info("Found existing contractor",
				slog.String("email", email),
				slog.Int64("contractorID", contractorID))
			return contractorID, nil
		}
		c.log.Debug("No contractor found with email", slog.String("email", email))
	} else {
		c.log.Debug("Error searching for contractor",
			slog.String("email", email),
			slog.String("error", err.Error()))
	}

	name := email
	if inv.Customer != nil && inv.Customer.Name != "" {
		name = inv.Customer.Name
	}

	// If not found, create a new contractor.
	c.log.Info("Creating new contractor", slog.String("email", email), slog.String("name", name))
	payload := map[string]interface{}{
		"contractors": []map[string]interface{}{
			{
				"contractor": map[string]interface{}{
					"name":          name,
					"email":         email,
					"tax_code_type": "other",
				},
			},
		},
	}
	createRes, err := c.request(ctx, "contractors", "add", payload)
	if err != nil {
		c.log.Error("Failed to create contractor",
			slog.String("email", email),
			slog.String("error", err.Error()))
		return 0, err
	}
	var addResp struct {
		Contractors []struct {
			ID int64 `json:"id"`
		} `json:"contractor"`
	}
	if err = json.Unmarshal(createRes, &addResp); err != nil {
		c.log.Error("Failed to parse contractor creation response", slog.String("error", err.Error()))
		return 0, err
	}
	if len(addResp.Contractors) == 0 {
		c.log.Error("Empty contractor add response")
		return 0, fmt.Errorf("empty contractor add response")
	}
	contractorID := addResp.Contractors[0].ID
	c.log.Info("Successfully created new contractor",
		slog.String("email", email),
		slog.String("name", name),
		slog.Int64("contractorID", contractorID))
	return contractorID, nil
}

// SyncInvoice creates/updates invoice in wFirma and attaches PDF.
func (c *Client) SyncInvoice(ctx context.Context, inv *stripe.Invoice, pdf []byte) error {
	c.log.Info("Starting invoice synchronization",
		slog.String("invoiceNumber", inv.Number),
		slog.String("customerEmail", inv.CustomerEmail))

	contractorID, err := c.getOrCreateContractor(ctx, inv)
	if err != nil {
		c.log.Error("Failed to get or create contractor",
			slog.String("invoiceNumber", inv.Number),
			slog.String("error", err.Error()))
		return fmt.Errorf("contractor: %w", err)
	}

	// Build contents from invoice lines.
	c.log.Debug("Building invoice contents",
		slog.String("invoiceNumber", inv.Number),
		slog.Int("lineCount", len(inv.Lines.Data)))

	var contents []map[string]interface{}
	for _, line := range inv.Lines.Data {
		contents = append(contents, map[string]interface{}{
			"invoicecontent": map[string]interface{}{
				"name":  line.Description,
				"count": line.Quantity,
				"price": float64(line.UnitAmountExcludingTax) / 100.0,
				"vat":   "23", // default VAT
			},
		})
	}

	iso := func(ts int64) string { return time.Unix(ts, 0).Format("2006-01-02") }

	c.log.Debug("Preparing invoice payload",
		slog.String("invoiceNumber", inv.Number),
		slog.String("currency", string(inv.Currency)),
		slog.String("sellDate", iso(inv.PeriodStart)),
		slog.String("issueDate", iso(inv.Created)))

	addPayload := map[string]interface{}{
		"invoices": []map[string]interface{}{
			{
				"invoice": map[string]interface{}{
					"contractor_id":   contractorID,
					"number":          inv.Number,
					"sell_date":       iso(inv.PeriodStart),
					"issue_date":      iso(inv.Created),
					"paymentdate":     iso(inv.DueDate),
					"paymentmethod":   "przelew",
					"currency":        strings.ToUpper(string(inv.Currency)),
					"lang":            "pl",
					"invoicecontents": contents,
				},
			},
		},
	}

	c.log.Info("Creating invoice in wFirma", slog.String("invoiceNumber", inv.Number))
	addRes, err := c.request(ctx, "invoices", "add", addPayload)
	if err != nil {
		c.log.Error("Failed to add invoice",
			slog.String("invoiceNumber", inv.Number),
			slog.String("error", err.Error()))
		return fmt.Errorf("add invoice: %w", err)
	}

	var addResp struct {
		Invoices struct {
			List []struct {
				ID int64 `json:"id"`
			} `json:"invoice"`
		} `json:"invoices"`
	}
	if err := json.Unmarshal(addRes, &addResp); err != nil {
		c.log.Error("Failed to parse invoice creation response",
			slog.String("invoiceNumber", inv.Number),
			slog.String("error", err.Error()))
		return err
	}
	if len(addResp.Invoices.List) == 0 {
		c.log.Error("No invoice ID returned from wFirma", slog.String("invoiceNumber", inv.Number))
		return fmt.Errorf("no invoice id returned")
	}

	invID := addResp.Invoices.List[0].ID
	c.log.Info("Invoice created successfully",
		slog.String("invoiceNumber", inv.Number),
		slog.Int64("wfirmaInvoiceID", invID))

	if len(pdf) == 0 {
		c.log.Debug("No PDF to attach", slog.String("invoiceNumber", inv.Number))
		return nil
	}

	c.log.Debug("Attaching PDF to invoice",
		slog.String("invoiceNumber", inv.Number),
		slog.Int("pdfSize", len(pdf)))

	attachPayload := map[string]interface{}{
		"invoices": []map[string]interface{}{
			{
				"invoice": map[string]interface{}{
					"id":         invID,
					"attachment": base64.StdEncoding.EncodeToString(pdf),
				},
			},
		},
	}

	if _, err := c.request(ctx, "invoices", "edit", attachPayload); err != nil {
		c.log.Error("Failed to attach PDF to invoice",
			slog.String("invoiceNumber", inv.Number),
			slog.Int64("wfirmaInvoiceID", invID),
			slog.String("error", err.Error()))
		return fmt.Errorf("attach pdf: %w", err)
	}

	c.log.Info("PDF attached successfully",
		slog.String("invoiceNumber", inv.Number),
		slog.Int64("wfirmaInvoiceID", invID))

	return nil
}
