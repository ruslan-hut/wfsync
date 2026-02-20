// Package wfirma integrates with the wFirma invoicing API (https://api2.wfirma.pl).
//
// The Client provides invoice creation (normal + proforma), PDF download,
// contractor management, goods catalog lookups, VAT code resolution, and
// bidirectional sync between local MongoDB and the remote wFirma account.
//
// File layout:
//
//	client.go      — Client struct, interfaces, constructor, HTTP transport
//	contractor.go  — contractor find/create operations
//	goods.go       — goods catalog (SKU) lookups
//	vat.go         — VAT code constants, resolution logic, vat_code caching
//	invoice.go     — invoice creation, download, payment registration
//	sync.go        — bidirectional sync between local DB and wFirma
//	entity.go      — request/response payload structs
//	response.go    — API response wrapper types
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
	"sync"
	"time"
	"wfsync/entity"
	"wfsync/internal/config"
	"wfsync/lib/sl"
)

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

// Database defines the persistence methods the wFirma client needs.
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

// Client is the wFirma API client. Use NewClient to create one.
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

// Config holds wFirma API credentials (currently unused — credentials come from config.Config).
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
