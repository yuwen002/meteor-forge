package sharding

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// ClickHouseTableManager 是 ClickHouse 数据库的表管理器实现
// 要求 CreateTable 的 SQL 模板必须显式包含 ENGINE 与 ORDER BY（ClickHouse 强制要求）
type ClickHouseTableManager struct {
	db *gorm.DB
}

func NewClickHouseTableManager(db *gorm.DB) *ClickHouseTableManager {
	return &ClickHouseTableManager{db: db}
}

var _ TableManager = (*ClickHouseTableManager)(nil)

func (m *ClickHouseTableManager) TableExists(ctx context.Context, table string) (bool, error) {
	if !isValidTableName(table) {
		return false, fmt.Errorf("无效的表名: %s", table)
	}
	var exists int
	query := `SELECT count() FROM system.tables WHERE database = currentDatabase() AND name = ?`
	err := m.db.WithContext(ctx).Raw(query, table).Scan(&exists).Error
	return exists > 0, err
}

func (m *ClickHouseTableManager) CreateTable(ctx context.Context, table, sql string) error {
	if !isValidTableName(table) {
		return fmt.Errorf("无效的表名: %s", table)
	}
	cleanSQL := cleanSQLTemplate(sql)
	if cleanSQL == "" {
		return fmt.Errorf("创建表失败: 空的表定义模板")
	}
	return m.db.WithContext(ctx).Exec(cleanSQL).Error
}

func (m *ClickHouseTableManager) CreateTableLike(ctx context.Context, newTable, sourceTable string) error {
	if !isValidTableName(newTable) {
		return fmt.Errorf("无效的新表名: %s", newTable)
	}
	if !isValidTableName(sourceTable) {
		return fmt.Errorf("无效的源表名: %s", sourceTable)
	}

	query := fmt.Sprintf("CREATE TABLE IF NOT EXISTS `%s` AS `%s`", newTable, sourceTable)
	return m.db.WithContext(ctx).Exec(query).Error
}

func (m *ClickHouseTableManager) DropTable(ctx context.Context, table string) error {
	if !isValidTableName(table) {
		return fmt.Errorf("无效的表名: %s", table)
	}
	return m.db.WithContext(ctx).Exec(fmt.Sprintf("DROP TABLE IF EXISTS `%s`", table)).Error
}

func (m *ClickHouseTableManager) ListTables(ctx context.Context, prefix string) ([]string, error) {
	var tables []string
	query := `SELECT name FROM system.tables WHERE database = currentDatabase() AND name LIKE ?`
	err := m.db.WithContext(ctx).Raw(query, prefix+"%").Scan(&tables).Error
	return tables, err
}