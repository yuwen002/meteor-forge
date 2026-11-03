package sqlite

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	_ "modernc.org/sqlite"
	"github.com/yuwen002/meteor-forge/pkg/migration"
)

func openSQLite(t *testing.T) (*sql.DB, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db, path
}

func TestSQLiteDriver_EnsureTable(t *testing.T) {
	db, _ := openSQLite(t)
	drv := NewDriver(db, "schema_migrations")

	ctx := context.Background()
	if err := drv.EnsureTable(ctx); err != nil {
		t.Fatalf("first EnsureTable: %v", err)
	}

	if err := drv.EnsureTable(ctx); err != nil {
		t.Fatalf("idempotent EnsureTable should not error: %v", err)
	}

	var count int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&count)
	if err != nil {
		t.Fatalf("table should exist: %v", err)
	}
}

func TestSQLiteDriver_GetAppliedVersions_Empty(t *testing.T) {
	db, _ := openSQLite(t)
	drv := NewDriver(db, "schema_migrations")

	ctx := context.Background()
	if err := drv.EnsureTable(ctx); err != nil {
		t.Fatal(err)
	}

	records, err := drv.GetAppliedVersions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("expected 0 records, got %d", len(records))
	}
}

func TestSQLiteDriver_ApplyMigration_Up(t *testing.T) {
	db, _ := openSQLite(t)
	drv := NewDriver(db, "schema_migrations")

	ctx := context.Background()
	if err := drv.EnsureTable(ctx); err != nil {
		t.Fatal(err)
	}

	script := &migration.Script{
		Version:     "V0001",
		Description: "create users",
		UpSQL:       `CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`,
		DownSQL:     `DROP TABLE users`,
	}

	if err := drv.ApplyMigration(ctx, script, false); err != nil {
		t.Fatalf("ApplyMigration up: %v", err)
	}

	records, err := drv.GetAppliedVersions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].Version != "V0001" {
		t.Errorf("expected V0001, got %s", records[0].Version)
	}

	var tableCount int
	err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='users'`).Scan(&tableCount)
	if err != nil {
		t.Fatal(err)
	}
	if tableCount != 1 {
		t.Error("users table should exist after up migration")
	}
}

func TestSQLiteDriver_ApplyMigration_Rollback(t *testing.T) {
	db, _ := openSQLite(t)
	drv := NewDriver(db, "schema_migrations")

	ctx := context.Background()
	if err := drv.EnsureTable(ctx); err != nil {
		t.Fatal(err)
	}

	script := &migration.Script{
		Version:     "V0001",
		Description: "create users",
		UpSQL:       `CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`,
		DownSQL:     `DROP TABLE users`,
	}

	if err := drv.ApplyMigration(ctx, script, false); err != nil {
		t.Fatal(err)
	}

	if err := drv.ApplyMigration(ctx, script, true); err != nil {
		t.Fatalf("ApplyMigration rollback: %v", err)
	}

	records, err := drv.GetAppliedVersions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("expected 0 records after rollback, got %d", len(records))
	}

	var tableCount int
	err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='users'`).Scan(&tableCount)
	if err != nil {
		t.Fatal(err)
	}
	if tableCount != 0 {
		t.Error("users table should be dropped after rollback")
	}
}

func TestSQLiteDriver_LockUnlock(t *testing.T) {
	db, _ := openSQLite(t)
	drv := NewDriver(db, "schema_migrations")

	ctx := context.Background()
	if err := drv.Lock(ctx); err != nil {
		t.Fatalf("Lock should be no-op for sqlite: %v", err)
	}
	if err := drv.Unlock(ctx); err != nil {
		t.Fatalf("Unlock should be no-op for sqlite: %v", err)
	}
	if err := drv.Unlock(ctx); err != nil {
		t.Fatalf("Unlock on non-held lock should still be safe: %v", err)
	}
}

func TestSQLiteDriver_CustomTableName(t *testing.T) {
	db, _ := openSQLite(t)
	drv := NewDriver(db, "my_versions")

	ctx := context.Background()
	if err := drv.EnsureTable(ctx); err != nil {
		t.Fatal(err)
	}

	script := &migration.Script{
		Version:     "V0001",
		Description: "test custom table",
		UpSQL:       `CREATE TABLE t1 (id INTEGER PRIMARY KEY)`,
		DownSQL:     `DROP TABLE t1`,
	}
	if err := drv.ApplyMigration(ctx, script, false); err != nil {
		t.Fatal(err)
	}

	var count int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM my_versions`).Scan(&count)
	if err != nil {
		t.Fatalf("custom table should exist: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 record, got %d", count)
	}
}

func TestSQLiteDriver_MigratorEndToEnd(t *testing.T) {
	db, _ := openSQLite(t)
	drv := NewDriver(db, "schema_migrations")

	ctx := context.Background()
	fsys := fstest.MapFS{
		"V0001__create_users.up.sql":  &fstest.MapFile{Data: []byte(`CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`)},
		"V0001__create_users.down.sql": &fstest.MapFile{Data: []byte(`DROP TABLE users`)},
		"V0002__add_email.up.sql":     &fstest.MapFile{Data: []byte(`ALTER TABLE users ADD COLUMN email TEXT`)},
		"V0002__add_email.down.sql":   &fstest.MapFile{Data: []byte(`SELECT 1`)},
	}
	loader := migration.NewFileSystemLoader(fsys, ".")

	migrator := migration.NewDefaultMigrator(loader, drv)

	if err := migrator.Up(ctx); err != nil {
		t.Fatalf("Up: %v", err)
	}

	records, _ := drv.GetAppliedVersions(ctx)
	if len(records) != 2 {
		t.Fatalf("expected 2 applied, got %d", len(records))
	}

	if err := migrator.DownSteps(ctx, 1); err != nil {
		t.Fatalf("DownSteps: %v", err)
	}
	records, _ = drv.GetAppliedVersions(ctx)
	if len(records) != 1 {
		t.Fatalf("expected 1 applied after rollback, got %d", len(records))
	}
}

func TestSQLiteDriver_FullFilePersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "persist.db")

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)

	drv := NewDriver(db, "schema_migrations")
	ctx := context.Background()

	if err := drv.EnsureTable(ctx); err != nil {
		t.Fatal(err)
	}
	script := &migration.Script{
		Version: "V0001", UpSQL: `CREATE TABLE x (id INTEGER)`, DownSQL: `DROP TABLE x`,
	}
	if err := drv.ApplyMigration(ctx, script, false); err != nil {
		t.Fatal(err)
	}
	db.Close()

	db2, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()

	drv2 := NewDriver(db2, "schema_migrations")
	records, err := drv2.GetAppliedVersions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("expected persisted record, got %d", len(records))
	}

	_ = os.Remove(path)
}