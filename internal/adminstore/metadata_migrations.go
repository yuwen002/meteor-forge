package adminstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// metadataSchemaMigration describes a historical metadata schema version.
// Versions 11-17 are intentionally retained as legacy bootstrap markers:
// their DDL remains in Store.migrate to preserve compatibility with databases
// created by earlier releases. New schema changes must be added as explicit
// migrations rather than extending the legacy bootstrap block.
type metadataSchemaMigration struct {
	Version int
	Name    string
}

var metadataSchemaMigrations = []metadataSchemaMigration{
	{Version: 11, Name: "管理元数据基础结构"},
	{Version: 12, Name: "安全审计记录"},
	{Version: 13, Name: "租户迁移批次审计"},
	{Version: 14, Name: "租户生命周期审计"},
	{Version: 15, Name: "租户连接健康记录"},
	{Version: 16, Name: "租户定时健康巡检"},
	{Version: 17, Name: "租户健康错误分类"},
}

func metadataSchemaVersion() int {
	if len(metadataSchemaMigrations) == 0 {
		return 0
	}
	return metadataSchemaMigrations[len(metadataSchemaMigrations)-1].Version
}

func validateMetadataMigrationRegistry() error {
	seenNames := make(map[string]struct{}, len(metadataSchemaMigrations))
	for i, migration := range metadataSchemaMigrations {
		if migration.Version <= 0 {
			return fmt.Errorf("metadata migration version must be positive: %d", migration.Version)
		}
		if strings.TrimSpace(migration.Name) == "" {
			return fmt.Errorf("metadata migration %d has an empty name", migration.Version)
		}
		if _, exists := seenNames[migration.Name]; exists {
			return fmt.Errorf("duplicate metadata migration name %q", migration.Name)
		}
		seenNames[migration.Name] = struct{}{}
		if i > 0 && migration.Version != metadataSchemaMigrations[i-1].Version+1 {
			return fmt.Errorf("metadata migration versions must be contiguous: %d follows %d",
				migration.Version, metadataSchemaMigrations[i-1].Version)
		}
	}
	if metadataSchemaVersion() != currentMetadataSchemaVersion {
		return fmt.Errorf("metadata migration registry latest version %d does not match supported version %d",
			metadataSchemaVersion(), currentMetadataSchemaVersion)
	}
	return nil
}

// recordMetadataSchemaMigrations writes the legacy version markers only after
// the idempotent legacy schema bootstrap has completed in the same transaction.
func recordMetadataSchemaMigrations(ctx context.Context, tx *sql.Tx) error {
	if err := validateMetadataMigrationRegistry(); err != nil {
		return fmt.Errorf("validate metadata migration registry: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, migration := range metadataSchemaMigrations {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO admin_schema_migrations
			(version, applied_at) VALUES (?, ?)`, migration.Version, now); err != nil {
			return fmt.Errorf("record metadata schema migration %d (%s): %w", migration.Version, migration.Name, err)
		}
	}
	return nil
}
