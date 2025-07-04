package database

import (
	"database/sql"
	"fmt"
	_ "github.com/go-sql-driver/mysql" // MySQL driver
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
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	var products []*entity.LineItem
	for rows.Next() {
		var product entity.LineItem
		if err = rows.Scan(
			&product.Name,
			&product.Price,
			&product.Qty,
			&product.Sku,
		); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		products = append(products, &product)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}

	return products, nil
}
