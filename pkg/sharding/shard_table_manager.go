package sharding

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// ShardTableManager is a thin facade: it composes a TableManager with a ShardRule,
// so callers work in terms of shard names (e.g. "202611", "7") instead of full table names.
type ShardTableManager struct {
	manager    TableManager
	baseName   string
	schemaTpl  string
	nameFormat func(string) string
}

// NewShardTableManager creates a ShardTableManager.
// fn is the shard-name -> full-table-name formatter; if nil, the shard name is used as-is.
func NewShardTableManager(manager TableManager, baseName, schemaTpl string, fn func(string) string) *ShardTableManager {
	if fn == nil {
		fn = func(shardName string) string {
			return shardName
		}
	}
	return &ShardTableManager{
		manager:    manager,
		baseName:   baseName,
		schemaTpl:  schemaTpl,
		nameFormat: fn,
	}
}

// CreateShardTable creates the shard table using the configured create-table template.
func (m *ShardTableManager) CreateShardTable(ctx context.Context, shardName string) error {
	tableName := m.nameFormat(shardName)
	return m.manager.CreateTable(ctx, tableName, m.schemaTpl)
}

// CreateShardTableLike clones the source shard's structure into a new shard.
func (m *ShardTableManager) CreateShardTableLike(ctx context.Context, newShardName, sourceShardName string) error {
	newTableName := m.nameFormat(newShardName)
	sourceTableName := m.nameFormat(sourceShardName)
	return m.manager.CreateTableLike(ctx, newTableName, sourceTableName)
}

// DropShardTable drops a shard table.
func (m *ShardTableManager) DropShardTable(ctx context.Context, shardName string) error {
	tableName := m.nameFormat(shardName)
	return m.manager.DropTable(ctx, tableName)
}

// TableExists reports whether the shard table exists.
func (m *ShardTableManager) TableExists(ctx context.Context, shardName string) (bool, error) {
	tableName := m.nameFormat(shardName)
	return m.manager.TableExists(ctx, tableName)
}

// ListShardTables lists every existing table that starts with baseName.
func (m *ShardTableManager) ListShardTables(ctx context.Context) ([]string, error) {
	return m.manager.ListTables(ctx, m.baseName)
}

// LoadTemplateFromFile reads the create-table template from disk.
func (m *ShardTableManager) LoadTemplateFromFile(filePath string) error {
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return fmt.Errorf("resolve absolute path: %v", err)
	}

	content, err := os.ReadFile(absPath)
	if err != nil {
		return fmt.Errorf("read template file: %v", err)
	}

	templateStr := string(content)
	if templateStr == "" {
		return fmt.Errorf("template file is empty")
	}

	m.schemaTpl = templateStr
	return nil
}

// LoadTemplateFromString sets the create-table template from an inline string.
func (m *ShardTableManager) LoadTemplateFromString(templateStr string) error {
	if templateStr == "" {
		return fmt.Errorf("template must not be empty")
	}
	m.schemaTpl = templateStr
	return nil
}

// GetCurrentTemplate returns the currently loaded create-table template.
func (m *ShardTableManager) GetCurrentTemplate() string {
	return m.schemaTpl
}