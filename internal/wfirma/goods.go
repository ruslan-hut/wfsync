package wfirma

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"wfsync/entity"
	"wfsync/lib/sl"
)

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
