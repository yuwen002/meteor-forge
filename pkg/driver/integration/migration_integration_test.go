//go:build integration

// Package integration exercises migration drivers against real database servers.
// Set METEOR_TEST_MYSQL_DSN, METEOR_TEST_POSTGRES_DSN, and
// METEOR_TEST_CLICKHOUSE_DSN to enable the corresponding server-backed tests.
package integration

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/ClickHouse/clickhouse-go/v2"
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"

	clickhousedriver "github.com/yuwen002/meteor-forge/pkg/driver/clickhouse"
	mysqldriver "github.com/yuwen002/meteor-forge/pkg/driver/mysql"
	postgresdriver "github.com/yuwen002/meteor-forge/pkg/driver/postgres"
	sqlitedriver "github.com/yuwen002/meteor-forge/pkg/driver/sqlite"
	"github.com/yuwen002/meteor-forge/pkg/migration"
)

func TestSQLiteMigrationLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migration.db")
	runMigrationLifecycle(t, "sqlite", path, "sqlite", func(db *sql.DB, table string) migration.Driver { return sqlitedriver.NewDriver(db, table) }, func(table string) string {
		return fmt.Sprintf("CREATE TABLE %s (id INTEGER PRIMARY KEY)", table)
	})
}

func TestMySQLMigrationLifecycle(t *testing.T) {
	dsn := os.Getenv("METEOR_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set METEOR_TEST_MYSQL_DSN to run MySQL integration tests")
	}
	runMigrationLifecycle(t, "mysql", dsn, "mysql", func(db *sql.DB, table string) migration.Driver { return mysqldriver.NewDriver(db, table) }, func(table string) string {
		return fmt.Sprintf("CREATE TABLE %s (id BIGINT PRIMARY KEY)", table)
	})
}

func TestPostgresMigrationLifecycle(t *testing.T) {
	dsn := os.Getenv("METEOR_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set METEOR_TEST_POSTGRES_DSN to run PostgreSQL integration tests")
	}
	runMigrationLifecycle(t, "pgx", dsn, "postgres", func(db *sql.DB, table string) migration.Driver { return postgresdriver.NewDriver(db, table) }, func(table string) string {
		return fmt.Sprintf("CREATE TABLE %s (id BIGINT PRIMARY KEY)", table)
	})
}

func TestClickHouseMigrationLifecycle(t *testing.T) {
	dsn := os.Getenv("METEOR_TEST_CLICKHOUSE_DSN")
	if dsn == "" {
		t.Skip("set METEOR_TEST_CLICKHOUSE_DSN to run ClickHouse integration tests")
	}
	runMigrationLifecycle(t, "clickhouse", dsn, "clickhouse", func(db *sql.DB, table string) migration.Driver { return clickhousedriver.NewDriver(db, table) }, func(table string) string {
		return fmt.Sprintf("CREATE TABLE %s (id Int64) ENGINE = MergeTree() ORDER BY id", table)
	})
}

type driverFactory func(*sql.DB, string) migration.Driver

func runMigrationLifecycle(
	t *testing.T,
	driverName string,
	dsn string,
	driverLabel string,
	newDriver driverFactory,
	createTableSQL func(string) string,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	db, err := sql.Open(driverName, dsn)
	if err != nil {
		t.Fatalf("open %s: %v", driverLabel, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping %s: %v", driverLabel, err)
	}

	suffix := fmt.Sprintf("%x", time.Now().UnixNano())
	historyTable := "mf_it_history_" + suffix
	targetTable := "mf_it_target_" + suffix
	drv := newDriver(db, historyTable)
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), "DROP TABLE IF EXISTS "+targetTable)
		_, _ = db.ExecContext(context.Background(), "DROP TABLE IF EXISTS "+historyTable)
	})

	if err := drv.EnsureTable(ctx); err != nil {
		t.Fatalf("%s EnsureTable: %v", driverLabel, err)
	}
	script := &migration.Script{
		Version:     "V0001",
		Description: "integration lifecycle",
		UpSQL:       createTableSQL(targetTable),
		DownSQL:     "DROP TABLE " + targetTable,
	}
	if err := drv.ApplyMigration(ctx, script, false); err != nil {
		t.Fatalf("%s apply up migration: %v", driverLabel, err)
	}

	records, err := drv.GetAppliedVersions(ctx)
	if err != nil {
		t.Fatalf("%s read applied versions: %v", driverLabel, err)
	}
	if len(records) != 1 {
		t.Fatalf("%s applied records = %d, want 1", driverLabel, len(records))
	}
	if records[0].Version != script.Version || records[0].Name != script.Description {
		t.Fatalf("%s migration record = %+v, want version %s and name %s", driverLabel, records[0], script.Version, script.Description)
	}
	if records[0].Checksum != script.Checksum() {
		t.Fatalf("%s checksum = %q, want %q", driverLabel, records[0].Checksum, script.Checksum())
	}

	if err := drv.ApplyMigration(ctx, script, true); err != nil {
		t.Fatalf("%s apply down migration: %v", driverLabel, err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		records, err = drv.GetAppliedVersions(ctx)
		if err != nil {
			t.Fatalf("%s read versions after rollback: %v", driverLabel, err)
		}
		if len(records) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s applied records after rollback = %d, want 0", driverLabel, len(records))
		}
		time.Sleep(100 * time.Millisecond)
	}
}
