package adminstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Historical versions 11-17 remain part of the legacy bootstrap. They are
// recorded after the idempotent bootstrap and are never replayed individually.
const legacyMetadataSchemaVersion = 17

// metadataSchemaMigration describes one metadata schema version. Legacy
// descriptors have no Up function; every version after the legacy boundary
// must provide an executable migration.
type metadataSchemaMigration struct {
	Version int
	Name    string
	Up      func(context.Context, *sql.Tx) error
}

var metadataSchemaMigrations = []metadataSchemaMigration{
	{Version: 11, Name: "管理元数据基础结构"},
	{Version: 12, Name: "安全审计记录"},
	{Version: 13, Name: "租户迁移批次审计"},
	{Version: 14, Name: "租户生命周期审计"},
	{Version: 15, Name: "租户连接健康记录"},
	{Version: 16, Name: "租户定时健康巡检"},
	{Version: 17, Name: "租户健康错误分类"},
	{Version: 18, Name: "迁移任务人工处置记录", Up: migrateMigrationTaskDispositions},
}

func metadataSchemaVersion() int {
	if len(metadataSchemaMigrations) == 0 {
		return 0
	}
	return metadataSchemaMigrations[len(metadataSchemaMigrations)-1].Version
}

func validateMetadataMigrationRegistry(migrations []metadataSchemaMigration, supportedVersion int) error {
	seenNames := make(map[string]struct{}, len(migrations))
	for i, migration := range migrations {
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
		if i > 0 && migration.Version != migrations[i-1].Version+1 {
			return fmt.Errorf("metadata migration versions must be contiguous: %d follows %d",
				migration.Version, migrations[i-1].Version)
		}
		if migration.Version <= legacyMetadataSchemaVersion && migration.Up != nil {
			return fmt.Errorf("legacy metadata migration %d must remain part of the bootstrap", migration.Version)
		}
		if migration.Version > legacyMetadataSchemaVersion && migration.Up == nil {
			return fmt.Errorf("metadata migration %d (%s) has no Up function", migration.Version, migration.Name)
		}
	}
	latest := 0
	if len(migrations) > 0 {
		latest = migrations[len(migrations)-1].Version
	}
	if latest != supportedVersion {
		return fmt.Errorf("metadata migration registry latest version %d does not match supported version %d",
			latest, supportedVersion)
	}
	return nil
}

// recordMetadataSchemaMigrations writes only legacy markers after the complete
// idempotent bootstrap has succeeded in the same transaction.
func recordMetadataSchemaMigrations(ctx context.Context, tx *sql.Tx) error {
	if err := validateMetadataMigrationRegistry(metadataSchemaMigrations, currentMetadataSchemaVersion); err != nil {
		return fmt.Errorf("validate metadata migration registry: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, migration := range metadataSchemaMigrations {
		if migration.Version > legacyMetadataSchemaVersion {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO admin_schema_migrations
			(version, applied_at) VALUES (?, ?)`, migration.Version, now); err != nil {
			return fmt.Errorf("record metadata schema migration %d (%s): %w", migration.Version, migration.Name, err)
		}
	}
	return nil
}

// applyMetadataSchemaMigrations runs each post-bootstrap migration in its own
// transaction. A no-op write obtains SQLite's writer lock before reading the
// current version, so concurrent starters re-check the version after waiting.
func applyMetadataSchemaMigrations(ctx context.Context, db *sql.DB, migrations []metadataSchemaMigration, supportedVersion int) error {
	if err := validateMetadataMigrationRegistry(migrations, supportedVersion); err != nil {
		return fmt.Errorf("validate metadata migration registry: %w", err)
	}
	for _, migration := range migrations {
		if migration.Version <= legacyMetadataSchemaVersion {
			continue
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin metadata migration %d (%s): %w", migration.Version, migration.Name, err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE admin_schema_migrations
			SET applied_at = applied_at
			WHERE version = (SELECT MAX(version) FROM admin_schema_migrations)`); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("lock metadata migration %d (%s): %w", migration.Version, migration.Name, err)
		}
		var current int
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM admin_schema_migrations").Scan(&current); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("read metadata schema version before migration %d: %w", migration.Version, err)
		}
		if current >= migration.Version {
			_ = tx.Rollback()
			continue
		}
		if current != migration.Version-1 {
			_ = tx.Rollback()
			return fmt.Errorf("cannot apply metadata migration %d (%s): current version is %d",
				migration.Version, migration.Name, current)
		}
		if err := migration.Up(ctx, tx); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply metadata migration %d (%s): %w", migration.Version, migration.Name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO admin_schema_migrations(version, applied_at)
			VALUES (?, ?)`, migration.Version, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record metadata migration %d (%s): %w", migration.Version, migration.Name, err)
		}
		if err := tx.Commit(); err != nil {
			// Some databases (including SQLite with deferred constraints) can
			// reject COMMIT while leaving the transaction active. Explicitly
			// roll it back so no partial schema state can leak into a retry.
			_ = tx.Rollback()
			return fmt.Errorf("commit metadata migration %d (%s): %w", migration.Version, migration.Name, err)
		}
	}
	return nil
}
