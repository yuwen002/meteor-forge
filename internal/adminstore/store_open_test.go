package adminstore

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestOpenAppliesSQLitePragmasToReplacementConnections(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "connection-pragmas.db")
	store, err := Open(ctx, path, testKey)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	// Force database/sql to discard its first physical connection. The next
	// query must receive a fresh connection with the same DSN-level settings.
	store.db.SetConnMaxLifetime(time.Nanosecond)
	time.Sleep(2 * time.Millisecond)

	var foreignKeys, busyTimeout int
	if err := store.db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatalf("read foreign_keys on replacement connection: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("read busy_timeout on replacement connection: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys on replacement connection = %d, want 1", foreignKeys)
	}
	if busyTimeout != 5000 {
		t.Fatalf("busy_timeout on replacement connection = %d, want 5000", busyTimeout)
	}
}

func TestOpenSerializesConcurrentInitializationOfSameDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "concurrent-open.db")

	const starters = 2
	start := make(chan struct{})
	type result struct {
		store *Store
		err   error
	}
	results := make(chan result, starters)
	var ready sync.WaitGroup
	ready.Add(starters)
	for i := 0; i < starters; i++ {
		go func() {
			ready.Done()
			<-start
			store, err := Open(ctx, path, testKey)
			results <- result{store: store, err: err}
		}()
	}
	ready.Wait()
	close(start)

	stores := make([]*Store, 0, starters)
	for i := 0; i < starters; i++ {
		select {
		case got := <-results:
			if got.err != nil {
				for _, opened := range stores {
					_ = opened.Close()
				}
				t.Fatalf("concurrent Open() %d failed: %v", i+1, got.err)
			}
			stores = append(stores, got.store)
		case <-time.After(15 * time.Second):
			for _, opened := range stores {
				_ = opened.Close()
			}
			t.Fatal("concurrent Open() did not finish within 15 seconds")
		}
	}
	for _, opened := range stores {
		defer opened.Close()
	}

	var versionCount, latestVersion int
	if err := stores[0].db.QueryRowContext(ctx,
		"SELECT COUNT(*), COALESCE(MAX(version), 0) FROM admin_schema_migrations").
		Scan(&versionCount, &latestVersion); err != nil {
		t.Fatalf("read initialized schema history: %v", err)
	}
	if versionCount != currentMetadataSchemaVersion-10 {
		t.Fatalf("schema history rows = %d, want %d", versionCount, currentMetadataSchemaVersion-10)
	}
	if latestVersion != currentMetadataSchemaVersion {
		t.Fatalf("latest schema version = %d, want %d", latestVersion, currentMetadataSchemaVersion)
	}
}


func TestOpenPreservesVersion17MetadataDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "version-17-metadata.db")

	// Build a representative v17 database, then close it to model an upgrade
	// from a previous process rather than reusing the original connection.
	legacy, err := Open(ctx, path, testKey)
	if err != nil {
		t.Fatalf("initialize v17 metadata database: %v", err)
	}
	seed := []string{
		`INSERT INTO admin_data_sources
			(id, name, driver, dsn_ciphertext, created_at, updated_at)
			VALUES ('ds-v17', 'legacy source', 'mysql', X'01020304', 'created', 'updated')`,
		`INSERT INTO admin_tenants
			(id, name, code, data_source_id, status, created_at, updated_at)
			VALUES ('tenant-v17', 'legacy tenant', 'legacy-v17', 'ds-v17', 'active', 'created', 'updated')`,
		`INSERT INTO admin_tenant_connection_health
			(tenant_id, status, checked_at, duration_ms, message, error_code)
			VALUES ('tenant-v17', 'unhealthy', '2026-11-09T10:00:00Z', 42, 'legacy health failure', 'connection_refused')`,
		`INSERT INTO admin_tenant_connection_health_history
			(tenant_id, source, status, checked_at, duration_ms, message, error_code)
			VALUES ('tenant-v17', 'scheduled', 'unhealthy', '2026-11-09T10:00:00Z', 42, 'legacy history', 'connection_refused')`,
		`UPDATE admin_tenant_health_settings
			SET enabled = 1, interval_seconds = 600, timeout_seconds = 8,
				history_retention_days = 45, updated_at = 'custom-v17-settings'
			WHERE id = 1`,
		`INSERT INTO admin_users
			(id, username, password_hash, role, created_at, updated_at)
			VALUES ('admin-v17', 'legacy-admin', 'existing-password-hash', 'administrator', 'created', 'updated')`,
		`INSERT INTO admin_security_audit_events
			(actor, subject, action, outcome, request_source, details, occurred_at)
			VALUES ('legacy-admin', 'tenant-v17', 'tenant.inspect', 'success', 'local', 'v17 audit', '2026-11-09T10:00:00Z')`,
	}
	for _, statement := range seed {
		if _, err := legacy.db.ExecContext(ctx, statement); err != nil {
			_ = legacy.Close()
			t.Fatalf("seed v17 metadata: %v", err)
		}
	}
	if _, err := legacy.db.ExecContext(ctx,
		"UPDATE admin_schema_migrations SET applied_at = 'v17-fixture-' || version"); err != nil {
		_ = legacy.Close()
		t.Fatalf("mark v17 migration history: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close v17 metadata database: %v", err)
	}

	upgraded, err := Open(ctx, path, testKey)
	if err != nil {
		t.Fatalf("reopen v17 metadata database: %v", err)
	}
	defer upgraded.Close()

	checks := []struct {
		name  string
		query string
		args  []any
		want  string
	}{
		{"data source", "SELECT name || '|' || driver || '|' || hex(dsn_ciphertext) FROM admin_data_sources WHERE id = ?", []any{"ds-v17"}, "legacy source|mysql|01020304"},
		{"tenant", "SELECT name || '|' || code || '|' || status FROM admin_tenants WHERE id = ?", []any{"tenant-v17"}, "legacy tenant|legacy-v17|active"},
		{"latest health", "SELECT status || '|' || error_code || '|' || message FROM admin_tenant_connection_health WHERE tenant_id = ?", []any{"tenant-v17"}, "unhealthy|connection_refused|legacy health failure"},
		{"health history", "SELECT source || '|' || status || '|' || error_code FROM admin_tenant_connection_health_history WHERE tenant_id = ?", []any{"tenant-v17"}, "scheduled|unhealthy|connection_refused"},
		{"health settings", "SELECT enabled || '|' || interval_seconds || '|' || timeout_seconds || '|' || history_retention_days || '|' || updated_at FROM admin_tenant_health_settings WHERE id = 1", nil, "1|600|8|45|custom-v17-settings"},
		{"administrator", "SELECT username || '|' || role || '|' || password_hash FROM admin_users WHERE id = ?", []any{"admin-v17"}, "legacy-admin|administrator|existing-password-hash"},
		{"security audit", "SELECT actor || '|' || action || '|' || details FROM admin_security_audit_events WHERE subject = ?", []any{"tenant-v17"}, "legacy-admin|tenant.inspect|v17 audit"},
	}
	for _, check := range checks {
		var got string
		if err := upgraded.db.QueryRowContext(ctx, check.query, check.args...).Scan(&got); err != nil {
			t.Fatalf("%s query failed: %v", check.name, err)
		}
		if got != check.want {
			t.Errorf("%s changed during v17 reopen: got %q, want %q", check.name, got, check.want)
		}
	}

	rows, err := upgraded.db.QueryContext(ctx, "SELECT version, applied_at FROM admin_schema_migrations ORDER BY version")
	if err != nil {
		t.Fatalf("read v17 migration history: %v", err)
	}
	defer rows.Close()
	expectedVersion := 11
	for rows.Next() {
		var version int
		var appliedAt string
		if err := rows.Scan(&version, &appliedAt); err != nil {
			t.Fatalf("scan v17 migration history: %v", err)
		}
		if version != expectedVersion || appliedAt != fmt.Sprintf("v17-fixture-%d", version) {
			t.Errorf("migration history row = (%d, %q), want (%d, %q)",
				version, appliedAt, expectedVersion, fmt.Sprintf("v17-fixture-%d", expectedVersion))
		}
		expectedVersion++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate v17 migration history: %v", err)
	}
	if expectedVersion != currentMetadataSchemaVersion+1 {
		t.Fatalf("migration history ended at version %d, want %d", expectedVersion-1, currentMetadataSchemaVersion)
	}

	var violations int
	if err := upgraded.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM pragma_foreign_key_check").Scan(&violations); err != nil {
		t.Fatalf("check v17 foreign keys after reopen: %v", err)
	}
	if violations != 0 {
		t.Fatalf("v17 database has %d foreign-key violations after reopen", violations)
	}
}
