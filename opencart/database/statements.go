package database

import (
	"database/sql"
	"fmt"
)

func (s *MySql) prepareStmt(name, query string) (*sql.Stmt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// если уже есть — возвращаем
	if stmt, ok := s.statements[name]; ok {
		return stmt, nil
	}

	// подготавливаем новый
	stmt, err := s.db.Prepare(query)
	if err != nil {
		return nil, fmt.Errorf("prepare statement [%s]: %w", name, err)
	}

	s.statements[name] = stmt
	return stmt, nil
}

func (s *MySql) closeStmt() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for name, stmt := range s.statements {
		_ = stmt.Close()
		delete(s.statements, name)
	}
}

func (s *MySql) stmtUpdateOrderStatus() (*sql.Stmt, error) {
	query := fmt.Sprintf(
		`UPDATE %sorder SET 
                   date_modified = ?,  
                   order_status_id = ?
                   WHERE order_id = ?`,
		s.prefix,
	)
	return s.prepareStmt("updateOrderStatus", query)
}

func (s *MySql) stmtUpdateOrderProforma() (*sql.Stmt, error) {
	query := fmt.Sprintf(
		`UPDATE %sorder SET   
                   wf_proforma = ?,
                   wf_file_proforma = ?
                   WHERE order_id = ?`,
		s.prefix,
	)
	return s.prepareStmt("updateOrderProforma", query)
}

func (s *MySql) stmtUpdateOrderInvoice() (*sql.Stmt, error) {
	query := fmt.Sprintf(
		`UPDATE %sorder SET   
                   wf_invoice = ?,
                   wf_file_invoice = ?
                   WHERE order_id = ?`,
		s.prefix,
	)
	return s.prepareStmt("stmtUpdateOrderInvoice", query)
}

func (s *MySql) stmtSelectOrderProducts() (*sql.Stmt, error) {
	query := fmt.Sprintf(
		`SELECT
			pd.name,
			op.total,
			op.tax,
			op.quantity,
			op.sku
		 FROM %sorder_product op
		 JOIN %sproduct_description pd ON op.product_id = pd.product_id 
		 WHERE op.order_id = ? AND pd.language_id = 2`,
		s.prefix, s.prefix,
	)
	return s.prepareStmt("selectOrderProducts", query)
}

func (s *MySql) stmtSelectOrderTotals() (*sql.Stmt, error) {
	query := fmt.Sprintf(
		`SELECT
			op.title,
			op.value
		 FROM %sorder_total op
		 WHERE op.order_id = ? AND op.code='shipping'`,
		s.prefix,
	)
	return s.prepareStmt("selectOrderTotals", query)
}

func (s *MySql) stmtSelectOrderStatus() (*sql.Stmt, error) {
	query := fmt.Sprintf(
		`SELECT
			order_id,
			firstname,
			lastname,
			email,
			telephone,
			custom_field,
			shipping_country,
			shipping_postcode,
			shipping_city,
			shipping_address_1,
			currency_code,
			wf_invoice,
			wf_proforma,
			total
		 FROM %sorder
		 WHERE order_status_id = ?
		 LIMIT 5`,
		s.prefix,
	)
	return s.prepareStmt("selectOrderStatus", query)
}
