package adminstore

import (
	"context"
	"database/sql"
	"fmt"
)

func migrateTenantHealthSchedulerRuns(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS admin_tenant_health_scheduler_runs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		lease_generation INTEGER NOT NULL CHECK(lease_generation > 0),
		started_at TEXT NOT NULL,
		finished_at TEXT,
		duration_ms INTEGER NOT NULL DEFAULT 0 CHECK(duration_ms >= 0),
		status TEXT NOT NULL CHECK(status IN ('running','succeeded','failed','lease_lost','interrupted')),
		error_code TEXT NOT NULL DEFAULT '',
		error_message TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		return fmt.Errorf("create tenant health scheduler runs table: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_admin_tenant_health_scheduler_runs_started
		ON admin_tenant_health_scheduler_runs(started_at DESC, id DESC)`); err != nil {
		return fmt.Errorf("index tenant health scheduler runs: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_admin_tenant_health_scheduler_runs_status
		ON admin_tenant_health_scheduler_runs(status, started_at DESC, id DESC)`); err != nil {
		return fmt.Errorf("index tenant health scheduler run status: %w", err)
	}
	return nil
}
