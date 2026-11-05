package adminapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yuwen002/meteor-forge/internal/adminstore"
)

func TestTenantMigrationTaskUsesTenantDataSourceAndPersistsScope(t *testing.T) {
	dir := t.TempDir()
	writeMigrationPair(t, dir, "000001_create_tenant_items",
		"CREATE TABLE tenant_items (id INTEGER PRIMARY KEY)",
		"DROP TABLE tenant_items",
	)
	store, err := adminstore.Open(context.Background(), ":memory:", apiTestKey)
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = store.Close() })

	target := filepath.Join(t.TempDir(), "tenant-target.db")
	source, err := store.Create(context.Background(), adminstore.DataSourceInput{
		Name: "tenant migration target", Driver: "sqlite", DSN: target,
	})
	if err != nil { t.Fatal(err) }
	tenant, err := store.CreateTenant(context.Background(), adminstore.TenantInput{
		Name: "Active tenant", Code: "active-tenant", DataSourceID: source.ID, Status: "active",
	})
	if err != nil { t.Fatal(err) }

	server := NewServerWithMigrationDir(store, dir)
	t.Cleanup(func() { _ = server.Close() })
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/tenants/"+tenant.ID+"/migrations/tasks", strings.NewReader(`{"action":"up"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("submit tenant migration status=%d body=%s", rec.Code, rec.Body.String())
	}
	var created adminstore.MigrationTask
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil { t.Fatal(err) }
	if created.TenantID != tenant.ID || created.DataSourceID != source.ID {
		t.Fatalf("task scope = tenant %q data source %q, want tenant %q data source %q",
			created.TenantID, created.DataSourceID, tenant.ID, source.ID)
	}
	waitForTaskStatus(t, store, created.ID, "succeeded")
	persisted, err := store.GetMigrationTask(context.Background(), created.ID)
	if err != nil { t.Fatal(err) }
	if persisted.TenantID != tenant.ID {
		t.Fatalf("persisted tenant ID = %q, want %q", persisted.TenantID, tenant.ID)
	}
}

func TestTenantMigrationTaskRejectsSuspendedTenant(t *testing.T) {
	dir := t.TempDir()
	store, err := adminstore.Open(context.Background(), ":memory:", apiTestKey)
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = store.Close() })
	source, err := store.Create(context.Background(), adminstore.DataSourceInput{
		Name: "suspended tenant target", Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "suspended.db"),
	})
	if err != nil { t.Fatal(err) }
	tenant, err := store.CreateTenant(context.Background(), adminstore.TenantInput{
		Name: "Suspended tenant", Code: "suspended-tenant", DataSourceID: source.ID, Status: "suspended",
	})
	if err != nil { t.Fatal(err) }

	server := NewServerWithMigrationDir(store, dir)
	t.Cleanup(func() { _ = server.Close() })
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/tenants/"+tenant.ID+"/migrations/tasks", strings.NewReader(`{"action":"up"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "tenant_suspended") {
		t.Fatalf("suspended tenant response status=%d body=%s", rec.Code, rec.Body.String())
	}
	items, err := store.ListMigrationTasks(context.Background(), 50)
	if err != nil { t.Fatal(err) }
	if len(items) != 0 { t.Fatalf("created %d migration tasks for suspended tenant", len(items)) }
}
