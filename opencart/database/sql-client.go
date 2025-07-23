package database

import (
	"database/sql"
	"fmt"
	_ "github.com/go-sql-driver/mysql" // MySQL driver
	"math"
	"strconv"
	"sync"
	"time"
	"wfsync/entity"
	"wfsync/internal/config"
)

const (
	totalCodeShipping = "shipping"
	totalCodeDiscount = "discount"
	//totalCodeTax      = "tax"
	//totalCodeTotal    = "total"
)

type MySql struct {
	db         *sql.DB
	loc        *time.Location
	prefix     string
	structure  map[string]map[string]Column
	statements map[string]*sql.Stmt
	nipId      string
	mu         sync.Mutex
}

func NewSQLClient(conf *config.Config) (*MySql, error) {
	if !conf.OpenCart.Enabled {
		return nil, fmt.Errorf("opencart client is disabled in configuration")
	}
	connectionURI := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?parseTime=true",
		conf.OpenCart.UserName, conf.OpenCart.Password, conf.OpenCart.HostName, conf.OpenCart.Port, conf.OpenCart.Database)
	db, err := sql.Open("mysql", connectionURI)
	if err != nil {
		return nil, fmt.Errorf("sql connect: %w", err)
	}

	// try to ping three times with a 30-second interval; wait for a database to start
	for i := 0; i < 3; i++ {
		if err = db.Ping(); err == nil {
			break
		}
		if i == 2 {
			return nil, fmt.Errorf("ping database: %w", err)
		}
		time.Sleep(30 * time.Second)
	}

	db.SetMaxOpenConns(50)           // макс. кол-во открытых соединений
	db.SetMaxIdleConns(10)           // макс. кол-во "неактивных" соединений в пуле
	db.SetConnMaxLifetime(time.Hour) // время жизни соединения

	sdb := &MySql{
		db:         db,
		prefix:     conf.OpenCart.Prefix,
		structure:  make(map[string]map[string]Column),
		statements: make(map[string]*sql.Stmt),
		nipId:      conf.OpenCart.CustomFieldNIP,
	}

	if err = sdb.addColumnIfNotExists("order", "wf_proforma", "VARCHAR(64) NOT NULL DEFAULT ''"); err != nil {
		return nil, err
	}
	if err = sdb.addColumnIfNotExists("order", "wf_invoice", "VARCHAR(64) NOT NULL DEFAULT ''"); err != nil {
		return nil, err
	}
	if err = sdb.addColumnIfNotExists("order", "wf_file_proforma", "VARCHAR(64) NOT NULL DEFAULT ''"); err != nil {
		return nil, err
	}
	if err = sdb.addColumnIfNotExists("order", "wf_file_invoice", "VARCHAR(64) NOT NULL DEFAULT ''"); err != nil {
		return nil, err
	}

	loc, err := time.LoadLocation(conf.Location)
	if err != nil {
		return nil, fmt.Errorf("load location: %w", err)
	}
	sdb.loc = loc

	return sdb, nil
}

func (s *MySql) Close() {
	s.closeStmt()
	_ = s.db.Close()
}

func (s *MySql) OrderProducts(orderId int64, currencyValue float64) ([]*entity.LineItem, error) {
	stmt, err := s.stmtSelectOrderProducts()
	if err != nil {
		return nil, err
	}
	rows, err := stmt.Query(orderId)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var products []*entity.LineItem
	for rows.Next() {
		var product entity.LineItem
		var total float64
		var tax float64
		if err = rows.Scan(
			&product.Name,
			&total, //here using the field 'total' - it's calculated with discount
			&tax,
			&product.Qty,
			&product.Sku,
		); err != nil {
			return nil, err
		}
		if product.Qty > 0 && total > 0 {
			// divide by quantity because 'total' contains row total value
			price := (total + tax) / float64(product.Qty)
			product.Price = int64(math.Round(price * currencyValue * 100))
			products = append(products, &product)
		}
	}

	if err = rows.Err(); err != nil {
		return nil, err
	}

	return products, nil
}

func (s *MySql) OrderShipping(orderId int64, currencyValue float64) (string, int64, error) {
	stmt, err := s.stmtSelectOrderTotals()
	if err != nil {
		return "", 0, err
	}
	rows, err := stmt.Query(orderId, totalCodeShipping)
	if err != nil {
		return "", 0, err
	}
	defer rows.Close()

	var title string
	var shipping float64
	for rows.Next() {
		if err = rows.Scan(
			&title,
			&shipping,
		); err != nil {
			return "", 0, err
		}
	}

	if err = rows.Err(); err != nil {
		return "", 0, err
	}

	return title, int64(math.Round(shipping * currencyValue * 100)), nil
}

func (s *MySql) OrderDiscount(orderId int64, currencyValue float64) (int64, error) {
	stmt, err := s.stmtSelectOrderTotals()
	if err != nil {
		return 0, err
	}
	rows, err := stmt.Query(orderId, totalCodeDiscount)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var title string
	var discount float64
	for rows.Next() {
		if err = rows.Scan(
			&title,
			&discount,
		); err != nil {
			return 0, err
		}
	}

	if err = rows.Err(); err != nil {
		return 0, err
	}

	return int64(math.Round(discount * currencyValue * 100)), nil
}

func (s *MySql) OrderSearchStatus(statusId int) ([]*entity.CheckoutParams, error) {
	stmt, err := s.stmtSelectOrderStatus()
	if err != nil {
		return nil, err
	}
	rows, err := stmt.Query(statusId)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	var orders []*entity.CheckoutParams
	for rows.Next() {

		var order entity.CheckoutParams
		var client entity.ClientDetails
		var customField string
		var firstName, lastName string
		var total float64

		if err = rows.Scan(
			&order.OrderId,
			&firstName,
			&lastName,
			&client.Email,
			&client.Phone,
			&customField,
			&client.Country,
			&client.ZipCode,
			&client.City,
			&client.Street,
			&order.Currency,
			&order.CurrencyValue,
			&order.InvoiceId,
			&order.InvoiceFile,
			&order.ProformaId,
			&order.ProformaFile,
			&total,
		); err != nil {
			return nil, err
		}

		// client data
		_ = client.ParseTaxId(s.nipId, customField)
		client.Name = firstName + " " + lastName
		order.ClientDetails = &client
		// order summary
		order.Total = int64(math.Round(total * order.CurrencyValue * 100))
		order.Created = time.Now().In(s.loc)
		order.Source = entity.SourceOpenCart

		orders = append(orders, &order)
	}

	if err = rows.Err(); err != nil {
		return nil, err
	}

	// add line items and shipping costs to each order
	for _, order := range orders {
		id, err := strconv.ParseInt(order.OrderId, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid order id: %s", order.OrderId)
		}
		order.LineItems, err = s.OrderProducts(id, order.CurrencyValue)
		if err != nil {
			return nil, fmt.Errorf("get order products: %w", err)
		}
		// discount must be added after products and before shipping to avoid discount on shipping
		discount, err := s.OrderDiscount(id, order.CurrencyValue)
		if err != nil {
			return nil, fmt.Errorf("get order discount: %w", err)
		}
		if discount > 0 {
			order.SetDiscount(discount)
		}
		title, value, err := s.OrderShipping(id, order.CurrencyValue)
		if err != nil {
			return nil, fmt.Errorf("get order shipping: %w", err)
		}
		if value > 0 {
			diff := order.Total - order.ItemsTotal() - value
			order.AddShipping(title, value+diff)
		} else {
			//_ = order.RefineTotal(0)
		}
	}

	return orders, nil
}

func (s *MySql) ChangeOrderStatus(orderId int64, orderStatusId int, comment string) error {
	stmt, err := s.stmtUpdateOrderStatus()
	if err != nil {
		return err
	}
	dateModified := time.Now().In(s.loc)

	// add order history record
	rec := map[string]interface{}{
		"order_id":        orderId,
		"order_status_id": orderStatusId,
		"notify":          0,
		"comment":         comment,
		"date_added":      dateModified,
	}
	_, err = s.insert("order_history", rec)
	if err != nil {
		return fmt.Errorf("insert order history: %w", err)
	}

	_, err = stmt.Exec(dateModified, orderStatusId, orderId)
	if err != nil {
		return err
	}
	return nil
}

func (s *MySql) UpdateProforma(orderId int64, proformaId, proformaFile string) error {
	stmt, err := s.stmtUpdateOrderProforma()
	if err != nil {
		return err
	}
	_, err = stmt.Exec(proformaId, proformaFile, orderId)
	if err != nil {
		return err
	}
	return nil
}

func (s *MySql) UpdateInvoice(orderId int64, invoiceId, invoiceFile string) error {
	stmt, err := s.stmtUpdateOrderInvoice()
	if err != nil {
		return err
	}
	_, err = stmt.Exec(invoiceId, invoiceFile, orderId)
	if err != nil {
		return err
	}
	return nil
}
