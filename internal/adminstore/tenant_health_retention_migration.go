package adminstore

import (
	"context"
	"database/sql"
	"fmt"
)

func migrateTenantHealthRetention(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS admin_tenant_health_retention_cleanup (
		id INTEGER PRIMARY KEY CHECK(id = 1),
		started_at TEXT,
		finished_at TEXT,
		status TEXT NOT NULL DEFAULT 'never_run' CHECK(status IN ('never_run','succeeded','failed')),
		deleted_history_count INTEGER NOT NULL DEFAULT 0 CHECK(deleted_history_count >= 0),
		deleted_run_count INTEGER NOT NULL DEFAULT 0 CHECK(deleted_run_count >= 0),
		error_code TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		return fmt.Errorf("create tenant health retention cleanup table: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO admin_tenant_health_retention_cleanup
		(id, status) VALUES (1, 'never_run')`); err != nil {
		return fmt.Errorf("initialize tenant health retention cleanup state: %w", err)
	}
	return nil
}
