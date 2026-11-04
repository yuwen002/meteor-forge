package adminapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/yuwen002/meteor-forge/internal/adminstore"
	sqlitedriver "github.com/yuwen002/meteor-forge/pkg/driver/sqlite"
	"github.com/yuwen002/meteor-forge/pkg/migration"
)

func TestMigrationStatusAndPreviewAreReadOnlyForTargetSchema(t *testing.T) {
	migrationDir := t.TempDir()
	writeMigrationPair(t, migrationDir, "000001_create_items",
		"CREATE TABLE items (id INTEGER PRIMARY KEY)",
		"DROP TABLE items",
	)
	writeMigrationPair(t, migrationDir, "000002_add_name",
		"ALTER TABLE items ADD COLUMN name TEXT",
		"SELECT 1",
	)

	targetDB := filepath.Join(t.TempDir(), "target.db")
	store, err := adminstore.Open(context.Background(), ":memory:", apiTestKey)
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = store.Close() })
	created, err := store.Create(context.Background(), adminstore.DataSourceInput{
		Name: "migration target", Driver: "sqlite", DSN: targetDB,
	})
	if err != nil { t.Fatal(err) }

	server := NewServerWithMigrationDir(store, migrationDir)
	t.Cleanup(func() { _ = server.Close() })

	statusReq := httptest.NewRequest(http.MethodGet, "/api/v1/datasources/"+created.ID+"/migrations/status", nil)
	statusRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(statusRec, statusReq)
	if statusRec.Code != http.StatusOK { t.Fatalf("status code=%d body=%s", statusRec.Code, statusRec.Body.String()) }

	var status migrationStatusResponse
	if err := json.Unmarshal(statusRec.Body.Bytes(), &status); err != nil { t.Fatal(err) }
	if status.PendingCount != 2 || status.AppliedCount != 0 || len(status.Items) != 2 {
		t.Fatalf("unexpected migration status: %+v", status)
	}

	previewReq := httptest.NewRequest(http.MethodPost, "/api/v1/datasources/"+created.ID+"/migrations/preview", nil)
	previewRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(previewRec, previewReq)
	if previewRec.Code != http.StatusOK { t.Fatalf("preview code=%d body=%s", previewRec.Code, previewRec.Body.String()) }

	var preview migrationPreviewResponse
	if err := json.Unmarshal(previewRec.Body.Bytes(), &preview); err != nil { t.Fatal(err) }
	if preview.ChangeCount != 2 || len(preview.Items) != 2 {
		t.Fatalf("unexpected migration preview: %+v", preview)
	}
	if preview.Items[0].Action != "up" || !preview.Items[0].Reversible {
		t.Fatalf("unexpected first preview item: %+v", preview.Items[0])
	}

	db, err := sql.Open("sqlite", targetDB)
	if err != nil { t.Fatal(err) }
	defer db.Close()
	var targetTableCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='items'").Scan(&targetTableCount); err != nil {
		t.Fatal(err)
	}
	if targetTableCount != 0 { t.Fatal("preview unexpectedly executed migration SQL") }
}

func TestMigrationPreviewBlocksChangedAppliedScript(t *testing.T) {
	migrationDir := t.TempDir()
	upSQL := "CREATE TABLE checksum_guard (id INTEGER PRIMARY KEY)"
	downSQL := "DROP TABLE checksum_guard"
	upPath := writeMigrationPair(t, migrationDir, "000001_checksum_guard", upSQL, downSQL)
	targetDB := filepath.Join(t.TempDir(), "target.db")

	store, err := adminstore.Open(context.Background(), ":memory:", apiTestKey)
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = store.Close() })
	created, err := store.Create(context.Background(), adminstore.DataSourceInput{
		Name: "checksum target", Driver: "sqlite", DSN: targetDB,
	})
	if err != nil { t.Fatal(err) }

	server := NewServerWithMigrationDir(store, migrationDir)
	t.Cleanup(func() { _ = server.Close() })

	// The first preview initializes the migration history table without applying the SQL.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/datasources/"+created.ID+"/migrations/preview", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK { t.Fatalf("initial preview code=%d body=%s", rec.Code, rec.Body.String()) }

	db, err := sql.Open("sqlite", targetDB)
	if err != nil { t.Fatal(err) }
	drv := sqlitedriver.NewDriver(db, "")
	script := &migration.Script{Version: "000001", Description: "checksum_guard", UpSQL: upSQL, DownSQL: downSQL}
	if err := drv.ApplyMigration(context.Background(), script, false); err != nil {
		_ = db.Close()
		t.Fatalf("seed applied migration: %v", err)
	}
	if err := db.Close(); err != nil { t.Fatal(err) }

	if err := os.WriteFile(upPath, []byte("CREATE TABLE checksum_guard (id INTEGER PRIMARY KEY, value TEXT)"), 0o600); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/datasources/"+created.ID+"/migrations/preview", nil)
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("changed applied migration preview code=%d body=%s, want 409", rec.Code, rec.Body.String())
	}

	if err := os.Remove(upPath); err != nil { t.Fatal(err) }
	if err := os.Remove(filepath.Join(migrationDir, "000001_checksum_guard.down.sql")); err != nil { t.Fatal(err) }
	req = httptest.NewRequest(http.MethodPost, "/api/v1/datasources/"+created.ID+"/migrations/preview", nil)
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("missing applied migration preview code=%d body=%s, want 409", rec.Code, rec.Body.String())
	}
}

func writeMigrationPair(t *testing.T, dir, base, upSQL, downSQL string) string {
	t.Helper()
	upPath := filepath.Join(dir, base+".up.sql")
	if err := os.WriteFile(upPath, []byte(upSQL), 0o600); err != nil { t.Fatal(err) }
	if err := os.WriteFile(filepath.Join(dir, base+".down.sql"), []byte(downSQL), 0o600); err != nil { t.Fatal(err) }
	return upPath
}
