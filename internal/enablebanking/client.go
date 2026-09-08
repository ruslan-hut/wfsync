// Package enablebanking integrates with the Enable Banking PSD2 aggregation API
// (https://api.enablebanking.com), which exposes bank account statements under the
// provider's AISP licence.
//
// It exists because wFirma, although connected to the bank itself, does not expose
// account movements through its API — so the fact that a customer paid an invoice can
// only be learned from the bank. Statements fetched here feed the payment matcher,
// which links incoming transfers to issued proformas and invoices.
//
// Authorization is an RS256 JWT signed with a locally held RSA private key and keyed
// by the application id; the key never leaves this service. Access to an account is
// granted by the account holder in person and lasts at most 180 days, with no refresh
// — an expired session can only be replaced by authorizing again.
//
// File layout:
//
//	client.go      — Client struct, constructor, JWT signing, HTTP transport
//	entity.go      — Enable Banking request/response payloads
//	transaction.go — conversion into the source-agnostic entity.BankTransaction
package enablebanking

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"wfsync/entity"
	"wfsync/internal/config"
	"wfsync/lib/sl"
)

// jwtTTL is how long a signed request token stays valid. Tokens are minted per
// request, so this only has to cover the call itself.
const jwtTTL = 5 * time.Minute

// jwtIssuer and jwtAudience are fixed by Enable Banking.
const (
	jwtIssuer   = "enablebanking.com"
	jwtAudience = "api.enablebanking.com"
)

// Client is the Enable Banking API client. Use New to create one.
type Client struct {
	enabled     bool
	baseURL     string
	appID       string
	redirectURL string
	aspspName   string
	aspspCtry   string
	psuType     string
	key         *rsa.PrivateKey
	hc          *http.Client
	log         *slog.Logger
}

// New builds a client from configuration and loads the RSA private key from disk.
//
// A disabled client is returned without a key and without an error, so the service
// starts normally when the integration is switched off. When enabled, a missing or
// malformed key is fatal to the client: every call would fail authorization anyway.
func New(conf *config.Config, logger *slog.Logger) (*Client, error) {
	c := &Client{
		enabled:     conf.EnableBanking.Enabled,
		baseURL:     strings.TrimRight(conf.EnableBanking.BaseURL, "/"),
		appID:       conf.EnableBanking.AppID,
		redirectURL: conf.EnableBanking.RedirectURL,
		aspspName:   conf.EnableBanking.ASPSPName,
		aspspCtry:   conf.EnableBanking.ASPSPCountry,
		psuType:     conf.EnableBanking.PSUType,
		hc:          &http.Client{Timeout: time.Duration(conf.EnableBanking.RequestTimeoutSec) * time.Second},
		log:         logger.With(sl.Module("enablebanking")),
	}
	if !c.enabled {
		return c, nil
	}
	if c.appID == "" {
		return nil, fmt.Errorf("enable_banking: app_id is empty")
	}
	key, err := loadPrivateKey(conf.EnableBanking.PrivateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("enable_banking: %w", err)
	}
	c.key = key
	c.log.Info("enable banking client ready",
		sl.Secret("app_id", c.appID),
		slog.String("aspsp", c.aspspName+"/"+c.aspspCtry),
		slog.String("psu_type", c.psuType))
	return c, nil
}

// Enabled reports whether the integration is switched on.
func (c *Client) Enabled() bool { return c.enabled }

// loadPrivateKey reads a PEM-encoded RSA private key in either PKCS#1 or PKCS#8 form.
func loadPrivateKey(path string) (*rsa.PrivateKey, error) {
	if path == "" {
		return nil, fmt.Errorf("private_key_path is empty")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read private key: %w", err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("private key %s: not PEM encoded", path)
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key %s: %w", path, err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key %s: not an RSA key", path)
	}
	return key, nil
}

// token mints an RS256 JWT for a single request. The application id travels in the
// "kid" header, which is how Enable Banking finds the certificate to verify against.
func (c *Client) token() (string, error) {
	now := time.Now()
	header := map[string]string{"typ": "JWT", "alg": "RS256", "kid": c.appID}
	claims := map[string]any{
		"iss": jwtIssuer,
		"aud": jwtAudience,
		"iat": now.Unix(),
		"exp": now.Add(jwtTTL).Unix(),
	}

	hb, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	cb, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}

	enc := base64.RawURLEncoding
	signing := enc.EncodeToString(hb) + "." + enc.EncodeToString(cb)

	hashed := crypto.SHA256.New()
	hashed.Write([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, c.key, crypto.SHA256, hashed.Sum(nil))
	if err != nil {
		return "", fmt.Errorf("sign jwt: %w", err)
	}
	return signing + "." + enc.EncodeToString(sig), nil
}

// request performs an authorized call and decodes the JSON response into out.
// A nil payload sends no body; a nil out discards the response.
func (c *Client) request(ctx context.Context, method, path string, payload, out any) error {
	if !c.enabled {
		return fmt.Errorf("enable banking is disabled")
	}

	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal payload: %w", err)
		}
		body = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	tok, err := c.token()
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("call %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode >= 300 {
		c.log.Error("enable banking api error",
			slog.String("path", path),
			slog.String("status", resp.Status),
			slog.String("body", string(data)))
		return c.apiError(resp.StatusCode, data)
	}

	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode %s response: %w", path, err)
	}
	return nil
}

// apiError turns an error response into a typed error, preserving the provider's
// message. A lapsed consent is reported as ErrSessionExpired so callers can trigger
// the re-authorization flow instead of retrying forever.
func (c *Client) apiError(status int, body []byte) error {
	var e APIError
	_ = json.Unmarshal(body, &e)
	msg := e.Message
	if msg == "" {
		msg = e.Error
	}
	if msg == "" {
		msg = string(body)
	}
	if status == http.StatusUnauthorized || strings.Contains(strings.ToUpper(msg+e.Code), "EXPIRED") {
		return fmt.Errorf("%w: %s", ErrSessionExpired, msg)
	}
	return fmt.Errorf("enable banking %d: %s", status, msg)
}

// ErrSessionExpired reports that the consent has lapsed and the account holder must
// authorize access again. Polling cannot recover from it on its own.
var ErrSessionExpired = fmt.Errorf("enable banking session expired")

// Application fetches the registered application. It is the cheapest call that proves
// the key pair and JWT signing are correct, and needs no bank session.
func (c *Client) Application(ctx context.Context) (*Application, error) {
	var app Application
	if err := c.request(ctx, http.MethodGet, "/application", nil, &app); err != nil {
		return nil, err
	}
	return &app, nil
}

// ASPSPs lists the bank connectors available to this application, optionally filtered
// by ISO country code. A sandbox application sees only the few banks whose test
// environments Enable Banking connected it to — PKO is not among them.
func (c *Client) ASPSPs(ctx context.Context, country string) ([]ASPSP, error) {
	path := "/aspsps"
	if country != "" {
		path += "?" + url.Values{"country": {country}}.Encode()
	}
	var list ASPSPList
	if err := c.request(ctx, http.MethodGet, path, nil, &list); err != nil {
		return nil, err
	}
	return list.ASPSPs, nil
}

// StartAuthorization begins account authorization and returns the URL the account
// holder has to open. validUntil caps the consent; the bank silently lowers it to its
// own maximum (180 days for PKO). state is echoed back to the redirect URL and should
// be used to tie the callback to the request that started it.
func (c *Client) StartAuthorization(ctx context.Context, state string, validUntil time.Time) (*AuthResponse, error) {
	if c.aspspName == "" {
		return nil, fmt.Errorf("enable_banking: aspsp_name is empty")
	}
	if c.redirectURL == "" {
		return nil, fmt.Errorf("enable_banking: redirect_url is empty")
	}
	req := AuthRequest{
		Access:      AuthAccess{ValidUntil: validUntil.UTC().Format(time.RFC3339)},
		ASPSP:       ASPSPRef{Name: c.aspspName, Country: c.aspspCtry},
		State:       state,
		RedirectURL: c.redirectURL,
		PSUType:     c.psuType,
	}
	var out AuthResponse
	if err := c.request(ctx, http.MethodPost, "/auth", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateSession exchanges the code delivered to the redirect URL for a session and
// the list of accounts it grants access to.
func (c *Client) CreateSession(ctx context.Context, code string) (*Session, error) {
	if code == "" {
		return nil, fmt.Errorf("authorization code is empty")
	}
	var s Session
	if err := c.request(ctx, http.MethodPost, "/sessions", SessionRequest{Code: code}, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Session fetches the current state of a session, including when its consent lapses.
func (c *Client) Session(ctx context.Context, sessionID string) (*Session, error) {
	var s Session
	if err := c.request(ctx, http.MethodGet, "/sessions/"+url.PathEscape(sessionID), nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// DeleteSession revokes a session. Used when re-authorizing, so a stale consent does
// not linger on the provider's side.
func (c *Client) DeleteSession(ctx context.Context, sessionID string) error {
	return c.request(ctx, http.MethodDelete, "/sessions/"+url.PathEscape(sessionID), nil, nil)
}

// Balances returns the balances reported for one account.
func (c *Client) Balances(ctx context.Context, accountUID string) ([]Balance, error) {
	var list BalanceList
	path := "/accounts/" + url.PathEscape(accountUID) + "/balances"
	if err := c.request(ctx, http.MethodGet, path, nil, &list); err != nil {
		return nil, err
	}
	return list.Balances, nil
}

// Transactions returns booked transactions for one account within [from, to],
// following pagination to the end. Dates are "YYYY-MM-DD"; an empty date is omitted
// and lets the bank apply its own default window.
//
// Only booked entries are requested: a pending entry can still change amount or
// vanish, so reconciling against one would produce payment facts that later turn out
// to be wrong.
func (c *Client) Transactions(ctx context.Context, accountUID, from, to string) ([]Transaction, error) {
	var all []Transaction
	continuation := ""

	for {
		q := url.Values{}
		if from != "" {
			q.Set("date_from", from)
		}
		if to != "" {
			q.Set("date_to", to)
		}
		q.Set("transaction_status", "BOOK")
		if continuation != "" {
			q.Set("continuation_key", continuation)
		}

		path := "/accounts/" + url.PathEscape(accountUID) + "/transactions?" + q.Encode()
		var page TransactionList
		if err := c.request(ctx, http.MethodGet, path, nil, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Transactions...)

		if page.ContinuationKey == "" {
			break
		}
		continuation = page.ContinuationKey

		// A bank that keeps returning the same key would otherwise loop forever.
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}

	c.log.Debug("transactions fetched",
		slog.String("account", accountUID),
		slog.String("from", from),
		slog.String("to", to),
		slog.Int("count", len(all)))
	return all, nil
}

// BankTransactions fetches an account's booked entries already normalized into the
// source-agnostic model the matcher consumes.
func (c *Client) BankTransactions(ctx context.Context, acc Account, from, to string) ([]*entity.BankTransaction, error) {
	txs, err := c.Transactions(ctx, acc.UID, from, to)
	if err != nil {
		return nil, err
	}
	out := make([]*entity.BankTransaction, 0, len(txs))
	for i := range txs {
		out = append(out, ToBankTransaction(&txs[i], acc.AccountID.IBAN))
	}
	// Keys are assigned over the whole window rather than per entry, so that two
	// entries the bank reports identically stay two rows instead of collapsing.
	entity.AssignKeys(out)
	return out, nil
}
