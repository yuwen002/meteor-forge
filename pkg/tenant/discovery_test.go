package tenant

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func openDiscoveryDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestLoadActiveTenantsWithDriverColumn(t *testing.T) {
	db := openDiscoveryDB(t)
	_, err := db.Exec(`CREATE TABLE t_tenants (
		tenant_id TEXT PRIMARY KEY, dsn TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'active', driver TEXT
	)`)
	if err != nil { t.Fatal(err) }
	_, err = db.Exec(`INSERT INTO t_tenants (tenant_id, dsn, status, driver) VALUES
		('tenant-a', 'sqlite://a.db', 'active', 'SQLITE'),
		('tenant-b', 'mysql://b', 'suspended', 'mysql')`)
	if err != nil { t.Fatal(err) }

	items, err := LoadActiveTenants(context.Background(), db)
	if err != nil { t.Fatal(err) }
	if len(items) != 1 || items[0].ID != "tenant-a" || items[0].Driver != "sqlite" {
		t.Fatalf("unexpected active tenants: %+v", items)
	}
}

func TestLoadActiveTenantsSupportsLegacyTableWithoutDriver(t *testing.T) {
	db := openDiscoveryDB(t)
	_, err := db.Exec(`CREATE TABLE t_tenants (
		tenant_id TEXT PRIMARY KEY, dsn TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'active'
	)`)
	if err != nil { t.Fatal(err) }
	_, err = db.Exec("INSERT INTO t_tenants (tenant_id, dsn, status) VALUES ('legacy', 'mysql://legacy', 'active')")
	if err != nil { t.Fatal(err) }

	items, err := LoadActiveTenants(context.Background(), db)
	if err != nil { t.Fatal(err) }
	if len(items) != 1 || items[0].ID != "legacy" || items[0].Driver != "" {
		t.Fatalf("unexpected legacy tenant result: %+v", items)
	}
}

func TestLoadActiveTenantsDoesNotHideInvalidDriverOrQueryErrors(t *testing.T) {
	db := openDiscoveryDB(t)
	_, err := db.Exec(`CREATE TABLE t_tenants (
		tenant_id TEXT PRIMARY KEY, dsn TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'active', driver TEXT
	)`)
	if err != nil { t.Fatal(err) }
	_, err = db.Exec("INSERT INTO t_tenants (tenant_id, dsn, status, driver) VALUES ('bad', 'mysql://bad', 'active', 'oracle')")
	if err != nil { t.Fatal(err) }
	if _, err := LoadActiveTenants(context.Background(), db); err == nil {
		t.Fatal("unsupported driver should not fall back to the legacy query")
	}

	missing := openDiscoveryDB(t)
	if _, err := LoadActiveTenants(context.Background(), missing); err == nil {
		t.Fatal("missing control table should return an error")
	}
}

func TestLoadActiveTenantsRejectsNilControlDatabase(t *testing.T) {
	if _, err := LoadActiveTenants(context.Background(), nil); err == nil {
		t.Fatal("nil control database should return an error")
	}
}
