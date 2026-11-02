package sharding

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"gorm.io/gorm"
)

// MySQLTableManager is the MySQL TableManager implementation.
type MySQLTableManager struct {
	db *gorm.DB
}

// NewMySQLTableManager creates a MySQL table manager instance.
func NewMySQLTableManager(db *gorm.DB) *MySQLTableManager {
	return &MySQLTableManager{db: db}
}

// TableExists checks whether the table exists.
func (m *MySQLTableManager) TableExists(ctx context.Context, table string) (bool, error) {
	var exists bool
	sql := "SELECT 1 FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ? LIMIT 1"
	err := m.db.WithContext(ctx).Raw(sql, table).Scan(&exists).Error
	return exists, err
}

// CreateTable creates a new table, auto-prepending CREATE TABLE IF NOT EXISTS
// and auto-replacing the table name when a full CREATE TABLE statement is supplied.
func (m *MySQLTableManager) CreateTable(ctx context.Context, table, sql string) error {
	if !isValidTableName(table) {
		return fmt.Errorf("invalid table name: %s", table)
	}

	cleanSQL := cleanSQLTemplate(sql)

	var createSQL string
	if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(cleanSQL)), "CREATE TABLE") {
		re := regexp.MustCompile(`(?i)CREATE\s+TABLE\s+` + "`" + `?\w+` + "`" + `?`)
		createSQL = re.ReplaceAllString(cleanSQL,
			fmt.Sprintf("CREATE TABLE IF NOT EXISTS "+"`%s`", table))
	} else {
		createSQL = fmt.Sprintf("CREATE TABLE IF NOT EXISTS "+"`%s` "+"%s", table, cleanSQL)
	}

	return m.db.WithContext(ctx).Exec(createSQL).Error
}

// CreateTableLike creates a new table with the same structure as the source table.
func (m *MySQLTableManager) CreateTableLike(ctx context.Context, newTable, sourceTable string) error {
	if !isValidTableName(newTable) {
		return fmt.Errorf("invalid table name: %s", newTable)
	}
	if !isValidTableName(sourceTable) {
		return fmt.Errorf("invalid source table name: %s", sourceTable)
	}

	sql := fmt.Sprintf("CREATE TABLE IF NOT EXISTS "+"`%s` LIKE `%s`", newTable, sourceTable)
	return m.db.WithContext(ctx).Exec(sql).Error
}

// DropTable drops the table (IF EXISTS avoids errors when it is missing).
func (m *MySQLTableManager) DropTable(ctx context.Context, table string) error {
	if !isValidTableName(table) {
		return fmt.Errorf("invalid table name: %s", table)
	}
	return m.db.WithContext(ctx).Exec(fmt.Sprintf("DROP TABLE IF EXISTS "+"`%s`", table)).Error
}

// ListTables lists all tables matching the prefix using a REGEXP filter.
func (m *MySQLTableManager) ListTables(ctx context.Context, prefix string) ([]string, error) {
	var tables []string
	sql := "SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name REGEXP ?"

	regexPattern := fmt.Sprintf("^%s(_\\d+)?$", regexp.QuoteMeta(prefix))
	err := m.db.WithContext(ctx).Raw(sql, regexPattern).Scan(&tables).Error
	return tables, err
}