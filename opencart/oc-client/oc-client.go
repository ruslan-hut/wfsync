package oc_client

import (
	"fmt"
	"strconv"
	"wfsync/entity"
	"wfsync/internal/config"
	"wfsync/opencart/database"
)

type Opencart struct {
	db *database.MySql
}

func New(conf *config.Config) (*Opencart, error) {
	if !conf.OpenCart.Enabled {
		return nil, nil
	}
	db, err := database.NewSQLClient(conf)
	if err != nil {
		return nil, fmt.Errorf("sql client: %w", err)
	}
	return &Opencart{db: db}, nil
}

func (oc *Opencart) OrderLines(orderId string) ([]*entity.LineItem, error) {
	if oc.db == nil || orderId == "" {
		return nil, nil
	}
	id, err := strconv.ParseInt(orderId, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid order id: %s", orderId)
	}
	items, err := oc.db.OrderProducts(id)
	if err != nil {
		return nil, fmt.Errorf("database query: %w", err)
	}
	return items, nil
}
