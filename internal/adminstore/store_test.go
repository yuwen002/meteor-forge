package adminstore

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

const testKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(context.Background(), ":memory:", testKey)
	if err != nil { t.Fatalf("Open() error = %v", err) }
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestDataSourceCRUDAndCredentialEncryption(t *testing.T) {
	store := newTestStore(t)
	input := DataSourceInput{Name: "local mysql", Driver: "mysql", DSN: "user:secret@tcp(localhost:3306)/app"}
	created, err := store.Create(context.Background(), input)
	if err != nil { t.Fatalf("Create() error = %v", err) }
	if created.ID == "" || created.Name != input.Name { t.Fatalf("unexpected created data source: %+v", created) }

	list, err := store.List(context.Background())
	if err != nil { t.Fatalf("List() error = %v", err) }
	if len(list) != 1 || list[0].ID != created.ID { t.Fatalf("unexpected list: %+v", list) }

	var stored []byte
	if err := store.db.QueryRowContext(context.Background(), "SELECT dsn_ciphertext FROM admin_data_sources WHERE id = ?", created.ID).Scan(&stored); err != nil { t.Fatalf("read stored ciphertext: %v", err) }
	if strings.Contains(string(stored), input.DSN) || strings.Contains(string(stored), "secret") { t.Fatal("DSN appears in plaintext in metadata database") }

	driver, dsn, err := store.ConnectionConfig(context.Background(), created.ID)
	if err != nil { t.Fatalf("ConnectionConfig() error = %v", err) }
	if driver != input.Driver || dsn != input.DSN { t.Fatalf("decrypted config mismatch: driver=%q dsn=%q", driver, dsn) }

	updated, err := store.Update(context.Background(), created.ID, DataSourceInput{
		Name: "renamed mysql", Driver: "mysql", DSN: "new-secret",
	})
	if err != nil { t.Fatalf("Update() error = %v", err) }
	if updated.Name != "renamed mysql" { t.Fatalf("updated name = %q", updated.Name) }

	if err := store.Delete(context.Background(), created.ID); err != nil { t.Fatalf("Delete() error = %v", err) }
	if _, err := store.Get(context.Background(), created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get() after delete error = %v, want ErrNotFound", err)
	}
}

func TestDataSourceValidationAndDuplicateName(t *testing.T) {
	store := newTestStore(t)
	invalid := []DataSourceInput{
		{Name: "", Driver: "mysql", DSN: "dsn"},
		{Name: "bad driver", Driver: "oracle", DSN: "dsn"},
		{Name: "missing dsn", Driver: "mysql"},
	}
	for _, input := range invalid {
		if _, err := store.Create(context.Background(), input); err == nil {
			t.Fatalf("Create(%+v) expected validation error", input)
		}
	}
	input := DataSourceInput{Name: "primary", Driver: "sqlite", DSN: ":memory:"}
	if _, err := store.Create(context.Background(), input); err != nil { t.Fatal(err) }
	if _, err := store.Create(context.Background(), input); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate Create() error = %v, want ErrConflict", err)
	}
}

func TestOpenRejectsInvalidEncryptionKey(t *testing.T) {
	if _, err := Open(context.Background(), ":memory:", "short"); err == nil {
		t.Fatal("Open() expected invalid encryption key error")
	}
}


func TestTenantHealthErrorCodeMigrationFromLegacySchemaIsIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy-metadata.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	legacySchema := []string{
		`CREATE TABLE admin_tenant_connection_health (
			tenant_id TEXT PRIMARY KEY,
			status TEXT NOT NULL CHECK(status IN ('healthy','unhealthy')),
			checked_at TEXT NOT NULL,
			duration_ms INTEGER NOT NULL DEFAULT 0,
			message TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE admin_tenant_connection_health_history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			tenant_id TEXT NOT NULL,
			source TEXT NOT NULL CHECK(source IN ('manual','scheduled')),
			status TEXT NOT NULL CHECK(status IN ('healthy','unhealthy')),
			checked_at TEXT NOT NULL,
			duration_ms INTEGER NOT NULL DEFAULT 0,
			message TEXT NOT NULL DEFAULT ''
		)`,
		`INSERT INTO admin_tenant_connection_health
			(tenant_id,status,checked_at,duration_ms,message)
			VALUES ('legacy-tenant','unhealthy','2026-11-08T10:00:00Z',12,'legacy failure')`,
		`INSERT INTO admin_tenant_connection_health_history
			(tenant_id,source,status,checked_at,duration_ms,message)
			VALUES ('legacy-tenant','manual','unhealthy','2026-11-08T10:00:00Z',12,'legacy failure')`,
	}
	for _, statement := range legacySchema {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			_ = db.Close()
			t.Fatalf("prepare legacy schema: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	for attempt := 0; attempt < 2; attempt++ {
		store, err := Open(ctx, path, testKey)
		if err != nil {
			t.Fatalf("Open() attempt %d: %v", attempt+1, err)
		}
		for _, table := range []string{"admin_tenant_connection_health", "admin_tenant_connection_health_history"} {
			var count int
			err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = 'error_code'", table).Scan(&count)
			if err != nil {
				_ = store.Close()
				t.Fatalf("inspect %s error_code column: %v", table, err)
			}
			if count != 1 {
				_ = store.Close()
				t.Fatalf("%s error_code column count=%d, want 1", table, count)
			}
		}
		var latestCode, historyCode string
		if err := store.db.QueryRowContext(ctx, "SELECT error_code FROM admin_tenant_connection_health WHERE tenant_id = 'legacy-tenant'").Scan(&latestCode); err != nil {
			_ = store.Close()
			t.Fatal(err)
		}
		if err := store.db.QueryRowContext(ctx, "SELECT error_code FROM admin_tenant_connection_health_history WHERE tenant_id = 'legacy-tenant'").Scan(&historyCode); err != nil {
			_ = store.Close()
			t.Fatal(err)
		}
		if latestCode != "unknown" || historyCode != "unknown" {
			_ = store.Close()
			t.Fatalf("legacy rows not safely backfilled: latest=%q history=%q", latestCode, historyCode)
		}
		var version17 int
		if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM admin_schema_migrations WHERE version = 17").Scan(&version17); err != nil {
			_ = store.Close()
			t.Fatal(err)
		}
		if version17 != 1 {
			_ = store.Close()
			t.Fatalf("schema version 17 count=%d, want 1", version17)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTenantHealthMessageNormalizationNeverPersistsRawDetails(t *testing.T) {
	secrets := []string{
		"postgres://root:secret@db.internal/app",
		"mysql://admin:p%40ss@db:3306/app",
		"password authentication failed for user admin: private stack trace",
		"driver error: dial tcp 10.0.0.1:5432: connection refused",
	}
	for _, input := range secrets {
		if got := normalizeTenantHealthMessage(input); got != "connection check failed; verify the registered data source" {
			t.Errorf("normalizeTenantHealthMessage(%q)=%q", input, got)
		}
	}
}


func TestOpenEnablesForeignKeysAndEnforcesRelationships(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	var enabled int
	if err := store.db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if enabled != 1 {
		t.Fatalf("PRAGMA foreign_keys=%d, want 1", enabled)
	}

	_, err := store.db.ExecContext(ctx, `INSERT INTO admin_tenants
		(id,name,code,data_source_id,status,created_at,updated_at)
		VALUES ('orphan-tenant','orphan','orphan','missing-source','active','now','now')`)
	if err == nil {
		t.Fatal("expected tenant insert with missing data source to fail")
	}

	if _, err := store.db.ExecContext(ctx, `INSERT INTO admin_data_sources
		(id,name,driver,dsn_ciphertext,created_at,updated_at)
		VALUES ('source-1','source','sqlite',X'01','now','now')`); err != nil {
		t.Fatalf("insert data source: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO admin_tenants
		(id,name,code,data_source_id,status,created_at,updated_at)
		VALUES ('tenant-1','tenant','tenant','source-1','active','now','now')`); err != nil {
		t.Fatalf("insert tenant: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "DELETE FROM admin_data_sources WHERE id = 'source-1'"); err == nil {
		t.Fatal("expected data source referenced by tenant to be protected")
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO admin_tenant_connection_health
		(tenant_id,status,checked_at,duration_ms,message)
		VALUES ('tenant-1','healthy','now',1,'')`); err != nil {
		t.Fatalf("insert tenant health: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "DELETE FROM admin_tenants WHERE id = 'tenant-1'"); err != nil {
		t.Fatalf("delete tenant: %v", err)
	}
	var healthRows int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM admin_tenant_connection_health WHERE tenant_id = 'tenant-1'").Scan(&healthRows); err != nil {
		t.Fatal(err)
	}
	if healthRows != 0 {
		t.Fatalf("cascaded health rows=%d, want 0", healthRows)
	}
	violations, err := store.CheckForeignKeys(ctx)
	if err != nil {
		t.Fatalf("CheckForeignKeys(): %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("unexpected foreign-key violations: %+v", violations)
	}
}

func TestCheckForeignKeysReportsLegacyOrphansWithoutDeletingData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy-orphan.db")
	store, err := Open(ctx, path, testKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	legacyDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacyDB.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		_ = legacyDB.Close()
		t.Fatal(err)
	}
	if _, err := legacyDB.ExecContext(ctx, `INSERT INTO admin_tenants
		(id,name,code,data_source_id,status,created_at,updated_at)
		VALUES ('legacy-orphan','legacy','legacy','missing-source','active','now','now')`); err != nil {
		_ = legacyDB.Close()
		t.Fatalf("seed legacy orphan: %v", err)
	}
	if err := legacyDB.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(ctx, path, testKey)
	if err != nil {
		t.Fatalf("Open() must preserve legacy data and allow inspection: %v", err)
	}
	defer store.Close()
	violations, err := store.CheckForeignKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 {
		t.Fatalf("foreign-key violations=%d, want 1: %+v", len(violations), violations)
	}
	if violations[0].Table != "admin_tenants" || violations[0].Parent != "admin_data_sources" {
		t.Fatalf("unexpected violation: %+v", violations[0])
	}
	var preserved int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM admin_tenants WHERE id='legacy-orphan'").Scan(&preserved); err != nil {
		t.Fatal(err)
	}
	if preserved != 1 {
		t.Fatalf("legacy orphan rows preserved=%d, want 1", preserved)
	}
}


func TestMetadataMigrationRegistryIsOrderedAndComplete(t *testing.T) {
	if err := validateMetadataMigrationRegistry(); err != nil {
		t.Fatalf("validateMetadataMigrationRegistry(): %v", err)
	}
	if got := metadataSchemaVersion(); got != currentMetadataSchemaVersion {
		t.Fatalf("metadataSchemaVersion()=%d, want %d", got, currentMetadataSchemaVersion)
	}
	if len(metadataSchemaMigrations) != currentMetadataSchemaVersion-metadataSchemaMigrations[0].Version+1 {
		t.Fatalf("migration registry has a gap: %+v", metadataSchemaMigrations)
	}
}


func TestOpenRejectsNewerMetadataSchemaWithoutModifyingIt(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "future-schema.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE admin_schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO admin_schema_migrations(version, applied_at)
		VALUES (18, 'future-release')`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, path, testKey)
	if err == nil {
		_ = store.Close()
		t.Fatal("Open() should reject a metadata database from a newer schema version")
	}
	if !strings.Contains(err.Error(), "metadata schema version 18 is newer than supported version 17") {
		t.Fatalf("unexpected Open() error: %v", err)
	}

	verifyDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer verifyDB.Close()
	var version int
	if err := verifyDB.QueryRowContext(ctx, "SELECT MAX(version) FROM admin_schema_migrations").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 18 {
		t.Fatalf("future schema version after rejected open=%d, want 18", version)
	}
	var dataSourceTableCount int
	if err := verifyDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'table' AND name = 'admin_data_sources'`).Scan(&dataSourceTableCount); err != nil {
		t.Fatal(err)
	}
	if dataSourceTableCount != 0 {
		t.Fatal("rejected open modified the newer metadata schema")
	}
}

func TestRepeatedOpenPreservesInitializedMetadata(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "repeated-open.db")
	for attempt := 0; attempt < 3; attempt++ {
		store, err := Open(ctx, path, testKey)
		if err != nil {
			t.Fatalf("Open() attempt %d: %v", attempt+1, err)
		}
		version, err := store.SchemaVersion(ctx)
		if err != nil {
			_ = store.Close()
			t.Fatal(err)
		}
		if version != 17 {
			_ = store.Close()
			t.Fatalf("schema version after open=%d, want 17", version)
		}
		if attempt == 0 {
			if _, err := store.db.ExecContext(ctx, `INSERT INTO admin_data_sources
				(id,name,driver,dsn_ciphertext,created_at,updated_at)
				VALUES ('persistent-source','persistent','sqlite',X'01','now','now')`); err != nil {
				_ = store.Close()
				t.Fatal(err)
			}
		}
		var count int
		if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM admin_data_sources WHERE id='persistent-source'").Scan(&count); err != nil {
			_ = store.Close()
			t.Fatal(err)
		}
		if attempt > 0 && count != 1 {
			_ = store.Close()
			t.Fatalf("persistent data count after restart=%d, want 1", count)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
