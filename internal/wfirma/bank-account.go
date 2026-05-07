package wfirma

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"wfsync/entity"
	"wfsync/lib/sl"
)

// bankAccountResponse mirrors the company_accounts/find response. The API
// returns numbered string keys ("0", "1", ...) plus a "parameters" key which
// we ignore by using RawMessage and skipping the non-numeric entry.
type bankAccountResponse struct {
	CompanyAccounts map[string]json.RawMessage `json:"company_accounts"`
	Status          struct {
		Code string `json:"code"`
	} `json:"status"`
}

// bankAccountWrapper is the inner object inside each numbered entry.
type bankAccountWrapper struct {
	CompanyAccount struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		BankName   string `json:"bank_name"`
		Number     string `json:"number"`
		Swift      string `json:"swift"`
		Currency   string `json:"currency"`
		Status     string `json:"status"`
		Visibility string `json:"visibility"`
	} `json:"company_account"`
}

// SyncBankAccounts fetches all company accounts from wFirma and upserts them
// into the local DB. The is_allowed flag is preserved across syncs (set only
// on first insert; manual operator toggles are never overwritten).
//
// Safe to call repeatedly. Returns (count synced, error).
func (c *Client) SyncBankAccounts(ctx context.Context) (int, error) {
	if !c.enabled {
		return 0, fmt.Errorf("wFirma is disabled")
	}
	if c.db == nil {
		return 0, fmt.Errorf("database not configured")
	}

	payload := map[string]interface{}{
		"api": map[string]interface{}{
			"company_accounts": map[string]interface{}{},
		},
	}

	res, err := c.request(ctx, "company_accounts", "find", payload)
	if err != nil {
		return 0, fmt.Errorf("fetch company accounts: %w", err)
	}

	var resp bankAccountResponse
	if err := json.Unmarshal(res, &resp); err != nil {
		return 0, fmt.Errorf("unmarshal company accounts: %w", err)
	}
	if resp.Status.Code != "OK" {
		return 0, fmt.Errorf("wFirma status: %s", resp.Status.Code)
	}

	now := time.Now()
	count := 0
	for key, raw := range resp.CompanyAccounts {
		// Skip the "parameters" pagination block — only numeric keys hold accounts.
		if key == "parameters" {
			continue
		}
		var wrapper bankAccountWrapper
		if err := json.Unmarshal(raw, &wrapper); err != nil {
			c.log.Warn("unmarshal company account entry", slog.String("key", key), sl.Err(err))
			continue
		}
		acc := wrapper.CompanyAccount
		if acc.ID == "" {
			continue
		}
		ba := &entity.BankAccount{
			ID:         acc.ID,
			Name:       acc.Name,
			BankName:   acc.BankName,
			Number:     acc.Number,
			Swift:      acc.Swift,
			Currency:   strings.ToUpper(acc.Currency),
			Status:     acc.Status,
			Visibility: acc.Visibility,
			SyncedAt:   now,
		}
		if err := c.db.SaveBankAccount(ba); err != nil {
			c.log.Warn("save bank account", slog.String("id", acc.ID), sl.Err(err))
			continue
		}
		count++
	}

	c.log.Info("bank accounts synced", slog.Int("count", count))
	return count, nil
}
