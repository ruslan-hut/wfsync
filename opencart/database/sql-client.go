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

type MySql struct {
	db         *sql.DB
	prefix     string
	structure  map[string]map[string]Column
	statements map[string]*sql.Stmt
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

	return sdb, nil
}

func (s *MySql) Close() {
	s.closeStmt()
	_ = s.db.Close()
}

func (s *MySql) OrderProducts(orderId int64) ([]*entity.LineItem, error) {
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
		var price float64
		var tax float64
		if err = rows.Scan(
			&product.Name,
			&price,
			&tax,
			&product.Qty,
			&product.Sku,
		); err != nil {
			return nil, err
		}
		product.Price = int64(math.Round((price + tax) * 100))
		products = append(products, &product)
	}

	if err = rows.Err(); err != nil {
		return nil, err
	}

	return products, nil
}

func (s *MySql) OrderShipping(orderId int64) (string, int64, error) {
	stmt, err := s.stmtSelectOrderTotals()
	if err != nil {
		return "", 0, err
	}
	rows, err := stmt.Query(orderId)
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

	return title, int64(math.Round(shipping * 100)), nil
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
		var firstName, lastName string
		if err = rows.Scan(
			&order.OrderId,
			&firstName,
			&lastName,
			&client.Email,
			&client.Phone,
			&client.Country,
			&client.ZipCode,
			&client.City,
			&client.Street,
			&order.Currency,
		); err != nil {
			return nil, err
		}
		client.Name = firstName + " " + lastName
		order.ClientDetails = &client
		order.Created = time.Now()
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
		order.LineItems, err = s.OrderProducts(id)
		if err != nil {
			return nil, fmt.Errorf("get order products: %w", err)
		}
		title, value, err := s.OrderShipping(id)
		if err != nil {
			return nil, fmt.Errorf("get order shipping: %w", err)
		}
		if value > 0 {
			order.AddShipping(title, value)
		}
		// calculate total
		for _, item := range order.LineItems {
			order.Total += item.Price * item.Qty
		}
	}

	return orders, nil
}

func (s *MySql) ChangeOrderStatus(orderId int64, orderStatusId int, comment string) error {
	stmt, err := s.stmtUpdateOrderStatus()
	if err != nil {
		return err
	}

	// add order history record
	rec := map[string]interface{}{
		"order_id":        orderId,
		"order_status_id": orderStatusId,
		"notify":          1, // notify customer
		"comment":         comment,
		"date_added":      time.Now(),
	}
	_, err = s.insert("order_history", rec)
	if err != nil {
		return fmt.Errorf("insert order history: %w", err)
	}

	dateModified := time.Now()
	_, err = stmt.Exec(dateModified, orderStatusId, orderId)
	if err != nil {
		return err
	}
	return nil
}
