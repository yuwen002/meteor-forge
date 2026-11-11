package adminstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

func migrateTenantHealthSchedulerLease(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS admin_tenant_health_scheduler_lease (
		id INTEGER PRIMARY KEY CHECK(id = 1),
		owner_id TEXT NOT NULL DEFAULT '',
		lease_until TEXT NOT NULL DEFAULT '',
		generation INTEGER NOT NULL DEFAULT 0 CHECK(generation >= 0),
		updated_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("create tenant health scheduler lease table: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO admin_tenant_health_scheduler_lease
		(id, owner_id, lease_until, generation, updated_at) VALUES (1, '', '', 0, ?)`,
		time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("initialize tenant health scheduler lease: %w", err)
	}
	return nil
}
