package database

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

type Column struct {
	Name          string  // Имя столбца
	DefaultValue  *string // Значение по умолчанию (nil, если в БД оно NULL)
	IsNullable    bool    // Разрешает ли столбец NULL
	DataType      string  // Тип данных (например, 'int', 'varchar' и т.д.)
	AutoIncrement bool    // Является ли столбец автоинкрементным
}

// loadTableStructure считывает структуру столбцов из information_schema
// и возвращает её в виде map[имя_колонки]ColumnInfo.
func (s *MySql) loadTableStructure(tableName string) (map[string]Column, error) {
	query := fmt.Sprintf(`
        SELECT COLUMN_NAME, COLUMN_DEFAULT, IS_NULLABLE, DATA_TYPE, EXTRA
          FROM information_schema.columns
         WHERE table_name = '%s%s'
         ORDER BY ORDINAL_POSITION`, s.prefix, tableName)

	rows, err := s.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to query columns: %w", err)
	}
	defer func(rows *sql.Rows) {
		_ = rows.Close()
	}(rows)

	columns := make(map[string]Column)

	for rows.Next() {
		var colName, isNullable, dataType, extra string
		var colDefault sql.NullString

		// Считываем строку
		if err = rows.Scan(&colName, &colDefault, &isNullable, &dataType, &extra); err != nil {
			return nil, fmt.Errorf("failed to scan column info: %w", err)
		}

		// Преобразуем флаг "YES"/"NO" в логический
		nullable := isNullable == "YES"

		var defValPtr *string
		if colDefault.Valid {
			defValPtr = &colDefault.String
		}

		columns[colName] = Column{
			Name:          colName,
			DefaultValue:  defValPtr,
			IsNullable:    nullable,
			DataType:      dataType,
			AutoIncrement: extra == "auto_increment",
		}
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("after scanning rows: %w", err)
	}

	return columns, nil
}

func (s *MySql) addColumnIfNotExists(tableName, columnName, columnType string) error {
	// Check if the column exists
	query := fmt.Sprintf(`SELECT COLUMN_NAME FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_NAME = '%s%s' AND COLUMN_NAME = '%s'`,
		s.prefix, tableName, columnName)
	var column string
	err := s.db.QueryRow(query).Scan(&column)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Column does not exist, so add it
			alterQuery := fmt.Sprintf(`ALTER TABLE %s%s ADD COLUMN %s %s`, s.prefix, tableName, columnName, columnType)
			_, err = s.db.Exec(alterQuery)
			if err != nil {
				return fmt.Errorf("add column %s to table %s: %w", columnName, tableName, err)
			}
		} else {
			return fmt.Errorf("checking column %s existence in %s: %w", columnName, tableName, err)
		}
	}
	return nil
}

func (s *MySql) readStructure(table string) (map[string]Column, error) {
	var err error
	// Запросим структуру таблицы из кэша
	if s.structure == nil {
		return nil, errors.New("structure cache is not initialized")
	}
	tableInfo, ok := s.structure[table]
	if !ok {
		tableInfo, err = s.loadTableStructure(table)
		if err != nil {
			return nil, fmt.Errorf("load table structure: %w", err)
		}
		s.structure[table] = tableInfo
	}
	return tableInfo, nil
}

func (s *MySql) insert(table string, userData map[string]interface{}) (int64, error) {

	// Получаем структуру таблицы
	tableInfo, err := s.readStructure(table)
	if err != nil {
		return 0, err
	}

	var colNames []string
	var placeholders []string
	var values []interface{}

	// Проходим по всем колонкам таблицы, которые были закешированы
	for colName, colInfo := range tableInfo {
		// Пропускаем колонки с AUTO_INCREMENT
		if colInfo.AutoIncrement {
			continue
		}
		// Смотрим, есть ли значение для этой колонки в userData
		if userVal, ok := userData[colName]; ok {
			// Пользовательская структура содержит данные — вставляем
			colNames = append(colNames, colName)
			placeholders = append(placeholders, "?")
			values = append(values, userVal)
		} else {
			// Данных от пользователя нет
			// Если у поля есть DEFAULT в БД — лучше пропустить, чтобы СУБД подставила дефолт
			if colInfo.DefaultValue != nil {
				continue
			}
			// Если поле допускает NULL, то тоже пропустим — тогда в БД попадёт NULL
			// (или будет использован DEFAULT NULL).
			if colInfo.IsNullable {
				continue
			}
			// Если мы здесь — поле NOT NULL, без DEFAULT => нужно что-то явно вставить
			colNames = append(colNames, colName)
			placeholders = append(placeholders, "?")

			switch colInfo.DataType {
			case "int", "bigint", "smallint", "tinyint", "decimal", "float", "double":
				values = append(values, 0)
			case "varchar", "text", "char", "blob":
				values = append(values, "")
			default:
				values = append(values, nil)
			}
		}
	}
	if len(colNames) == 0 {
		return 0, fmt.Errorf("no columns found in table %s", table)
	}
	// Формируем сам запрос INSERT
	insertSQL := fmt.Sprintf(
		"INSERT INTO %s%s (%s) VALUES (%s)",
		s.prefix,
		table,
		strings.Join(colNames, ", "),
		strings.Join(placeholders, ", "),
	)
	res, err := s.db.Exec(insertSQL, values...)
	if err != nil {
		return 0, fmt.Errorf("%s insert: %w", table, err)
	}

	// Get the last inserted product_id
	rowId, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("%s get last insert id: %v", table, err)
	}

	return rowId, nil
}
