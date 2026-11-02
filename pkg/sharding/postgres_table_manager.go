package sharding

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// PostgresTableManager 是 PostgreSQL 数据库的表管理器实现
type PostgresTableManager struct {
	db *gorm.DB
}

func NewPostgresTableManager(db *gorm.DB) *PostgresTableManager {
	return &PostgresTableManager{db: db}
}

var _ TableManager = (*PostgresTableManager)(nil)

func (m *PostgresTableManager) TableExists(ctx context.Context, table string) (bool, error) {
	if !isValidTableName(table) {
		return false, fmt.Errorf("无效的表名: %s", table)
	}
	var exists int
	query := `SELECT COUNT(*) FROM information_schema.tables
              WHERE table_schema = current_schema() AND table_name = $1`
	err := m.db.WithContext(ctx).Raw(query, table).Scan(&exists).Error
	return exists > 0, err
}

func (m *PostgresTableManager) CreateTable(ctx context.Context, table, sql string) error {
	if !isValidTableName(table) {
		return fmt.Errorf("无效的表名: %s", table)
	}
	cleanSQL := cleanSQLTemplate(sql)
	if cleanSQL == "" {
		return fmt.Errorf("创建表失败: 空的表定义模板")
	}
	return m.db.WithContext(ctx).Exec(cleanSQL).Error
}

func (m *PostgresTableManager) CreateTableLike(ctx context.Context, newTable, sourceTable string) error {
	if !isValidTableName(newTable) {
		return fmt.Errorf("无效的新表名: %s", newTable)
	}
	if !isValidTableName(sourceTable) {
		return fmt.Errorf("无效的源表名: %s", sourceTable)
	}

	query := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS "%s" (LIKE "%s" INCLUDING ALL)`, newTable, sourceTable)
	err := m.db.WithContext(ctx).Exec(query).Error
	if err == nil {
		return nil
	}

	fallback := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS "%s" AS TABLE "%s" WITH NO DATA`, newTable, sourceTable)
	return m.db.WithContext(ctx).Exec(fallback).Error
}

func (m *PostgresTableManager) DropTable(ctx context.Context, table string) error {
	if !isValidTableName(table) {
		return fmt.Errorf("无效的表名: %s", table)
	}
	return m.db.WithContext(ctx).Exec(fmt.Sprintf(`DROP TABLE IF EXISTS "%s"`, table)).Error
}

func (m *PostgresTableManager) ListTables(ctx context.Context, prefix string) ([]string, error) {
	var tables []string
	query := `SELECT table_name FROM information_schema.tables
              WHERE table_schema = current_schema() AND table_name LIKE $1`
	err := m.db.WithContext(ctx).Raw(query, prefix+"%").Scan(&tables).Error
	return tables, err
}