package adminapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yuwen002/meteor-forge/internal/adminstore"
)

func TestTenantDiscoveryPreviewIsReadOnlyAndDoesNotExposeDSN(t *testing.T) {
	ctx := context.Background()
	store, err := adminstore.Open(ctx, ":memory:", apiTestKey)
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = store.Close() })

	target, err := store.Create(ctx, adminstore.DataSourceInput{Name: "target", Driver: "sqlite", DSN: ":memory:"})
	if err != nil { t.Fatal(err) }
	_, err = store.CreateTenant(ctx, adminstore.TenantInput{Name: "Existing", Code: "existing", DataSourceID: target.ID})
	if err != nil { t.Fatal(err) }

	controlPath := filepath.Join(t.TempDir(), "control.db")
	controlDB, err := sql.Open("sqlite", controlPath)
	if err != nil { t.Fatal(err) }
	_, err = controlDB.Exec(`CREATE TABLE t_tenants (
		tenant_id TEXT PRIMARY KEY, dsn TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'active', driver TEXT
	)`)
	if err != nil { t.Fatal(err) }
	_, err = controlDB.Exec(`INSERT INTO t_tenants (tenant_id, dsn, status, driver) VALUES
		('new-tenant', 'mysql://user:secret@host/db', 'active', 'mysql'),
		('existing', 'mysql://other:secret@host/db', 'active', 'mysql'),
		('suspended', 'mysql://suspended:secret@host/db', 'suspended', 'mysql')`)
	if err != nil { t.Fatal(err) }
	if err := controlDB.Close(); err != nil { t.Fatal(err) }

	control, err := store.Create(ctx, adminstore.DataSourceInput{Name: "control", Driver: "sqlite", DSN: controlPath})
	if err != nil { t.Fatal(err) }
	server := newTestServerWithStore(store)
	t.Cleanup(func() { _ = server.Close() })

	request := httptest.NewRequest(http.MethodPost, "/api/v1/tenant-discovery/preview",
		strings.NewReader(`{"control_data_source_id":"`+control.ID+`"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "secret") || strings.Contains(response.Body.String(), "mysql://") || strings.Contains(response.Body.String(), "dsn") {
		t.Fatalf("preview must not expose discovered credentials: %s", response.Body.String())
	}
	var result tenantDiscoveryPreview
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil { t.Fatal(err) }
	if result.Total != 2 || result.New != 1 || result.AlreadyRegistered != 1 || len(result.Items) != 2 {
		t.Fatalf("unexpected preview summary: %+v", result)
	}
	if result.Items[0].ExternalID != "existing" || result.Items[0].Action != "already_registered" || result.Items[0].RegisteredStatus != "active" {
		t.Fatalf("existing tenant was not matched: %+v", result.Items[0])
	}
	if result.Items[1].ExternalID != "new-tenant" || result.Items[1].Action != "create" {
		t.Fatalf("new tenant was not identified: %+v", result.Items[1])
	}
	items, err := store.ListTenants(ctx)
	if err != nil { t.Fatal(err) }
	if len(items) != 1 || items[0].Code != "existing" {
		t.Fatalf("preview must not mutate the local tenant registry: %+v", items)
	}
}

func TestTenantDiscoveryPreviewRejectsMissingControlTableWithoutLeakingDetails(t *testing.T) {
	ctx := context.Background()
	store, err := adminstore.Open(ctx, ":memory:", apiTestKey)
	if err != nil { t.Fatal(err) }
	defer store.Close()
	path := filepath.Join(t.TempDir(), "empty.db")
	db, err := sql.Open("sqlite", path)
	if err != nil { t.Fatal(err) }
	if err := db.Close(); err != nil { t.Fatal(err) }
	source, err := store.Create(ctx, adminstore.DataSourceInput{Name: "empty control", Driver: "sqlite", DSN: path})
	if err != nil { t.Fatal(err) }
	server := newTestServerWithStore(store)
	defer server.Close()

	request := httptest.NewRequest(http.MethodPost, "/api/v1/tenant-discovery/preview",
		strings.NewReader(`{"control_data_source_id":"`+source.ID+`"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "tenant_discovery_failed") {
		t.Fatalf("unexpected discovery failure response: status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "no such table") || strings.Contains(response.Body.String(), path) {
		t.Fatalf("discovery error leaked database details: %s", response.Body.String())
	}
}

func TestTenantDiscoveryPreviewRolePolicy(t *testing.T) {
	for _, role := range []string{"viewer", "migration_operator", "shard_operator"} {
		if !authorizeAdminRole(role, http.MethodPost, "/api/v1/tenant-discovery/preview") {
			t.Errorf("role %s should be allowed to run a read-only preview", role)
		}
	}
	if !authorizeAdminRole("administrator", http.MethodPost, "/api/v1/tenant-discovery/preview") {
		t.Fatal("administrator should be allowed to preview tenant discovery")
	}
	if authorizeAdminRole("viewer", http.MethodPost, "/api/v1/tenant-discovery/sync") {
		t.Fatal("viewer must not be allowed to synchronize tenants")
	}
	_ = os.ErrNotExist
}
