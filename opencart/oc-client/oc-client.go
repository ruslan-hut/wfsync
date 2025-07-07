package oc_client

import (
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"
	"wfsync/entity"
	"wfsync/internal/config"
	"wfsync/lib/sl"
	"wfsync/opencart/database"
)

type UrlRequestHandler func(params *entity.CheckoutParams) (*entity.Payment, error)

type Opencart struct {
	db               *database.MySql
	log              *slog.Logger
	statusUrlRequest int
	statusUrlResult  int
	handlerUrl       UrlRequestHandler
	mutex            sync.Mutex
}

func New(conf *config.Config, log *slog.Logger) (*Opencart, error) {
	if !conf.OpenCart.Enabled {
		return nil, nil
	}
	db, err := database.NewSQLClient(conf)
	if err != nil {
		return nil, fmt.Errorf("sql client: %w", err)
	}
	oc := &Opencart{
		db:  db,
		log: log.With(sl.Module("opencart")),
	}
	if conf.OpenCart.StatusUrlRequest != "" {
		oc.statusUrlRequest, _ = strconv.Atoi(conf.OpenCart.StatusUrlRequest)
	}
	if conf.OpenCart.StatusUrlResult != "" {
		oc.statusUrlResult, _ = strconv.Atoi(conf.OpenCart.StatusUrlResult)
	}
	return oc, nil
}

func (oc *Opencart) WithUrlHandler(handler UrlRequestHandler) *Opencart {
	oc.handlerUrl = handler
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			oc.ProcessOrders()
			<-ticker.C
		}
	}()
	return oc
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
	title, value, err := oc.db.OrderShipping(id)
	if value > 0 {
		items = append(items, entity.ShippingLineItem(title, value))
	}
	return items, nil
}

func (oc *Opencart) ProcessOrders() {
	oc.mutex.Lock()
	defer oc.mutex.Unlock()

	if oc.statusUrlRequest > 0 && oc.handlerUrl != nil {
		orders, err := oc.db.OrderSearchStatus(oc.statusUrlRequest)
		if err != nil {
			oc.log.With(
				slog.Int("status", oc.statusUrlRequest),
				sl.Err(err),
			).Error("get orders by status")
			return
		}
		if len(orders) == 0 {
			return
		}
		oc.log.With(
			slog.Int("status", oc.statusUrlRequest),
			slog.Int("count", len(orders)),
		).Debug("process orders by status")
		for _, order := range orders {
			if order == nil || order.OrderId == "" {
				continue
			}
			payment, err := oc.handlerUrl(order)
			if err != nil {
				oc.log.With(
					slog.String("order_id", order.OrderId),
					sl.Err(err),
				).Error("handle order url request")
				continue
			}
			if payment == nil {
				continue
			}
			orderId, err := strconv.ParseInt(order.OrderId, 10, 64)
			if err != nil {
				oc.log.With(
					slog.String("order_id", order.OrderId),
					sl.Err(err),
				).Error("invalid order id")
				continue
			}
			comment := fmt.Sprintf("<a href=\"%s\" target=\"_blank\">Stripe Pay Link</a>", payment.Link)
			err = oc.db.ChangeOrderStatus(orderId, oc.statusUrlResult, comment)
			if err != nil {
				oc.log.With(
					slog.String("order_id", order.OrderId),
					slog.Int("status", oc.statusUrlResult),
					sl.Err(err),
				).Error("change order status")
				continue
			}
			oc.log.With(
				slog.String("order_id", order.OrderId),
			).Debug("link request processed")
		}
	}
}
