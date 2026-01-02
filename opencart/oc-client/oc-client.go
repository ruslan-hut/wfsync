package oc_client

import (
	"context"
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

type JobType string

const (
	JobStripeLink JobType = "stripe-pay-link"
	JobProforma   JobType = "wfirma-proforma"
	JobInvoice    JobType = "wfirma-invoice"
)

type CheckoutHandler func(ctx context.Context, params *entity.CheckoutParams) (*entity.Payment, error)

type Opencart struct {
	db                    *database.MySql
	log                   *slog.Logger
	statusUrlRequest      int
	statusUrlResult       int
	statusProformaRequest int
	statusProformaResult  int
	statusInvoiceRequest  int
	statusInvoiceResult   int
	handlerUrl            CheckoutHandler
	handlerProforma       CheckoutHandler
	handlerInvoice        CheckoutHandler
	mutex                 sync.Mutex
	done                  chan struct{}
	stopped               chan struct{}
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

	parseStatus := func(name, value string) int {
		if value == "" {
			return 0
		}
		v, err := strconv.Atoi(value)
		if err != nil {
			oc.log.Warn("invalid status config value",
				slog.String("field", name),
				slog.String("value", value),
				sl.Err(err))
			return 0
		}
		return v
	}

	oc.statusUrlRequest = parseStatus("status_url_request", conf.OpenCart.StatusUrlRequest)
	oc.statusUrlResult = parseStatus("status_url_result", conf.OpenCart.StatusUrlResult)
	oc.statusProformaRequest = parseStatus("status_proforma_request", conf.OpenCart.StatusProformaRequest)
	oc.statusProformaResult = parseStatus("status_proforma_result", conf.OpenCart.StatusProformaResult)
	oc.statusInvoiceRequest = parseStatus("status_invoice_request", conf.OpenCart.StatusInvoiceRequest)
	oc.statusInvoiceResult = parseStatus("status_invoice_result", conf.OpenCart.StatusInvoiceResult)

	return oc, nil
}

func (oc *Opencart) Start() {
	oc.done = make(chan struct{})
	oc.stopped = make(chan struct{})
	go func() {
		ticker := time.NewTicker(3 * time.Minute)
		defer ticker.Stop()
		defer close(oc.stopped)
		for {
			oc.ProcessOrders()
			select {
			case <-oc.done:
				oc.log.Info("order processor stopped")
				return
			case <-ticker.C:
			}
		}
	}()
}

func (oc *Opencart) Stop() {
	if oc.done != nil {
		oc.log.Info("stopping order processor")
		close(oc.done)
		<-oc.stopped
	}
	if oc.db != nil {
		oc.db.Close()
	}
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

	order, err := oc.db.OrderSearchId(id)
	if err != nil {
		return nil, fmt.Errorf("database query: %w", err)
	}

	return order.LineItems, nil
}

func (oc *Opencart) ProcessOrders() {
	oc.mutex.Lock()
	defer oc.mutex.Unlock()

	oc.handleByStatus(oc.statusUrlRequest, oc.statusUrlResult, oc.handlerUrl, JobStripeLink)

	oc.handleByStatus(oc.statusProformaRequest, oc.statusProformaResult, oc.handlerProforma, JobProforma)

	oc.handleByStatus(oc.statusInvoiceRequest, oc.statusInvoiceResult, oc.handlerInvoice, JobInvoice)
}

// handleByStatus processes orders based on the given status and applies the provided handler to update their state.
func (oc *Opencart) handleByStatus(statusRequest, statusResult int, handler CheckoutHandler, jobName JobType) {
	if statusRequest == 0 || handler == nil {
		return
	}
	log := oc.log.With(
		slog.String("job", string(jobName)),
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

	for _, order := range orders {
		if order == nil || order.OrderId == "" {
			continue
		}

		linesTotal := order.ItemsTotal()
		// warn if the order total does not match a sum of line items (for debugging)
		if order.Total != linesTotal {
			log.With(
				slog.String("order_id", order.OrderId),
				slog.Int64("total", order.Total),
				slog.Int64("lines_total", linesTotal),
				slog.Int64("diff", order.Total-linesTotal),
			).Warn("order total mismatch")
		}

		orderId, err := strconv.ParseInt(order.OrderId, 10, 64)
		if err != nil {
			log.With(
				slog.String("order_id", order.OrderId),
				sl.Err(err),
			).Error("invalid order id")
			continue
		}

		// clear status history
		err = oc.db.ClearStatusHistory(orderId, statusRequest)
		if err != nil {
			log.With(
				slog.String("order_id", order.OrderId),
				slog.Int("status", statusRequest),
				sl.Err(err),
			).Warn("clear status history")
		}
		err = oc.db.ClearStatusHistory(orderId, statusResult)
		if err != nil {
			log.With(
				slog.String("order_id", order.OrderId),
				slog.Int("status", statusResult),
				sl.Err(err),
			).Warn("clear status history")
		}

		// Use a context with timeout for background processing
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		payment, err := handler(ctx, order)
		cancel()
		if err != nil {
			log.With(
				slog.String("order_id", order.OrderId),
				sl.Err(err),
			).Error("handle order")
			_ = oc.db.ChangeOrderStatus(orderId, statusResult, fmt.Sprintf("Error: %v", err))
			continue
		}
		if payment == nil {
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

		if jobName == JobProforma {
			err = oc.UpdateOrderWithProforma(orderId, payment.Id, payment.InvoiceFile)
			if err != nil {
				log.With(
					slog.String("order_id", order.OrderId),
					sl.Err(err),
				).Error("update proforma")
			}
		}
		if jobName == JobInvoice {
			err = oc.UpdateOrderWithInvoice(orderId, payment.Id, payment.InvoiceFile)
			if err != nil {
				log.With(
					slog.String("order_id", order.OrderId),
					sl.Err(err),
				).Error("update invoice")
			}
		}

		log.With(
			slog.String("order_id", order.OrderId),
		).Debug("order processed")
	}
}

func (oc *Opencart) GetOrder(orderId int64) (*entity.CheckoutParams, error) {
	if oc.db == nil {
		return nil, fmt.Errorf("database not connected")
	}
	return oc.db.OrderSearchId(orderId)
}

func (oc *Opencart) SaveInvoiceId(orderId string, invoiceId, invoiceFile string) error {
	if oc.db == nil || orderId == "" {
		return nil
	}
	id, err := strconv.ParseInt(orderId, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid order id: %s", orderId)
	}
	return oc.db.UpdateInvoice(id, invoiceId, invoiceFile)
}

func (oc *Opencart) UpdateOrderWithProforma(orderId int64, proformaId, proformaFile string) error {
	return oc.db.UpdateProforma(orderId, proformaId, proformaFile)
}

func (oc *Opencart) UpdateOrderWithInvoice(orderId int64, proformaId, proformaFile string) error {
	return oc.db.UpdateInvoice(orderId, proformaId, proformaFile)
}
