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

type CheckoutHandler func(params *entity.CheckoutParams) (*entity.Payment, error)

type Opencart struct {
	db               *database.MySql
	log              *slog.Logger
	statusUrlRequest int
	statusUrlResult  int
	handlerUrl       CheckoutHandler
	handlerProforma  CheckoutHandler
	handlerInvoice   CheckoutHandler
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

func (oc *Opencart) Start() {
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			oc.ProcessOrders()
			<-ticker.C
		}
	}()
}

func (oc *Opencart) WithUrlHandler(handler CheckoutHandler) *Opencart {
	oc.handlerUrl = handler
	return oc
}

func (oc *Opencart) WithProformaHandler(handler CheckoutHandler) *Opencart {
	oc.handlerProforma = handler
	return oc
}

func (oc *Opencart) WithInvoiceHandler(handler CheckoutHandler) *Opencart {
	oc.handlerInvoice = handler
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

	oc.handleByStatus(oc.statusUrlRequest, oc.statusUrlResult, oc.handlerUrl, "stripe-pay-link")

}

func (oc *Opencart) handleByStatus(statusRequest, statusResult int, handler CheckoutHandler, jobName string) {
	if statusRequest == 0 || handler == nil {
		return
	}
	log := oc.log.With(
		slog.String("job", jobName),
		slog.Int("status", statusRequest),
	)

	orders, err := oc.db.OrderSearchStatus(statusRequest)
	if err != nil {
		log.With(
			sl.Err(err),
		).Error("get orders")
		return
	}
	if len(orders) == 0 {
		return
	}

	log.With(
		slog.Int("count", len(orders)),
	).Debug("processing orders")

	for _, order := range orders {
		if order == nil || order.OrderId == "" {
			continue
		}

		payment, err := handler(order)
		if err != nil {
			log.With(
				slog.String("order_id", order.OrderId),
				sl.Err(err),
			).Error("handle order")
			continue
		}
		if payment == nil {
			continue
		}

		orderId, err := strconv.ParseInt(order.OrderId, 10, 64)
		if err != nil {
			log.With(
				slog.String("order_id", order.OrderId),
				sl.Err(err),
			).Error("invalid order id")
			continue
		}

		if statusResult == 0 {
			statusResult = statusRequest + 1
		}

		comment := fmt.Sprintf("<a href=\"%s\" target=\"_blank\">%s</a>", payment.Link, jobName)
		err = oc.db.ChangeOrderStatus(orderId, statusResult, comment)
		if err != nil {
			log.With(
				slog.String("order_id", order.OrderId),
				slog.Int("status_result", statusResult),
				sl.Err(err),
			).Error("change order status")
			continue
		}
	}
}
