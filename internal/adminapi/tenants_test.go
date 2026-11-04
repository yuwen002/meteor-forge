package adminapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yuwen002/meteor-forge/internal/adminstore"
)

func TestTenantManagementAPI(t *testing.T) {
	store, err := adminstore.Open(context.Background(), ":memory:", apiTestKey)
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = store.Close() })
	source, err := store.Create(context.Background(), adminstore.DataSourceInput{
		Name: "tenant api db", Driver: "sqlite", DSN: ":memory:",
	})
	if err != nil { t.Fatal(err) }
	server := NewServerWithStore(store)
	t.Cleanup(func() { _ = server.Close() })

	request := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, req)
		return rec
	}

	createBody := `{"name":"Acme tenant","code":"ACME","data_source_id":"` + source.ID + `"}`
	created := request(http.MethodPost, "/api/v1/tenants", createBody)
	if created.Code != http.StatusCreated { t.Fatalf("create status=%d body=%s", created.Code, created.Body.String()) }
	var tenant adminstore.Tenant
	if err := json.Unmarshal(created.Body.Bytes(), &tenant); err != nil { t.Fatal(err) }
	if tenant.Code != "acme" || tenant.Status != "active" || tenant.DataSourceID != source.ID {
		t.Fatalf("unexpected tenant response: %+v", tenant)
	}
	if strings.Contains(strings.ToLower(created.Body.String()), "dsn") {
		t.Fatalf("tenant response must not expose connection credentials: %s", created.Body.String())
	}

	listed := request(http.MethodGet, "/api/v1/tenants", "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), tenant.ID) {
		t.Fatalf("list status=%d body=%s", listed.Code, listed.Body.String())
	}
	got := request(http.MethodGet, "/api/v1/tenants/"+tenant.ID, "")
	if got.Code != http.StatusOK { t.Fatalf("get status=%d body=%s", got.Code, got.Body.String()) }

	tested := request(http.MethodPost, "/api/v1/tenants/"+tenant.ID+"/test", "")
	if tested.Code != http.StatusOK || !strings.Contains(tested.Body.String(), `"success":true`) {
		t.Fatalf("test connection status=%d body=%s", tested.Code, tested.Body.String())
	}

	task, err := store.CreateMigrationTask(context.Background(), adminstore.MigrationTask{
		ID: "tenant-api-active-task", DataSourceID: source.ID, Action: "up",
	})
	if err != nil { t.Fatal(err) }
	_ = task
	activeUpdate := request(http.MethodPut, "/api/v1/tenants/"+tenant.ID,
		`{"name":"Paused","code":"acme","data_source_id":"`+source.ID+`","status":"suspended"}`)
	if activeUpdate.Code != http.StatusConflict || !strings.Contains(activeUpdate.Body.String(), "tenant_has_active_migration") {
		t.Fatalf("tenant update during migration status=%d body=%s", activeUpdate.Code, activeUpdate.Body.String())
	}
	activeDelete := request(http.MethodDelete, "/api/v1/tenants/"+tenant.ID, "")
	if activeDelete.Code != http.StatusConflict || !strings.Contains(activeDelete.Body.String(), "tenant_has_active_migration") {
		t.Fatalf("tenant delete during migration status=%d body=%s", activeDelete.Code, activeDelete.Body.String())
	}
	finished := time.Now().UTC()
	if err := store.UpdateMigrationTask(context.Background(), "tenant-api-active-task", "canceled", "canceled", nil, &finished); err != nil {
		t.Fatal(err)
	}

	duplicate := request(http.MethodPost, "/api/v1/tenants", createBody)
	if duplicate.Code != http.StatusConflict || !strings.Contains(duplicate.Body.String(), "tenant_conflict") {
		t.Fatalf("duplicate status=%d body=%s", duplicate.Code, duplicate.Body.String())
	}
	if rec := request(http.MethodPut, "/api/v1/datasources/"+source.ID,
		`{"name":"changed","driver":"sqlite","dsn":":memory:"}`); rec.Code != http.StatusConflict ||
		!strings.Contains(rec.Body.String(), "data_source_has_tenants") {
		t.Fatalf("data source update status=%d body=%s", rec.Code, rec.Body.String())
	}

	update := request(http.MethodPut, "/api/v1/tenants/"+tenant.ID,
		`{"name":"Acme paused","code":"acme","data_source_id":"`+source.ID+`","status":"suspended"}`)
	if update.Code != http.StatusOK || !strings.Contains(update.Body.String(), `"status":"suspended"`) {
		t.Fatalf("tenant update status=%d body=%s", update.Code, update.Body.String())
	}
	removed := request(http.MethodDelete, "/api/v1/tenants/"+tenant.ID, "")
	if removed.Code != http.StatusNoContent { t.Fatalf("tenant delete status=%d body=%s", removed.Code, removed.Body.String()) }
	if rec := request(http.MethodDelete, "/api/v1/datasources/"+source.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("data source delete after tenant removal status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestTenantAPIRejectsInvalidAndMissingDataSource(t *testing.T) {
	store, err := adminstore.Open(context.Background(), ":memory:", apiTestKey)
	if err != nil { t.Fatal(err) }
	defer store.Close()
	server := NewServerWithStore(store)
	defer server.Close()

	invalid := httptest.NewRequest(http.MethodPost, "/api/v1/tenants",
		strings.NewReader(`{"name":"Tenant","code":"bad/code","data_source_id":"missing"}`))
	invalidRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(invalidRec, invalid)
	if invalidRec.Code != http.StatusBadRequest || !strings.Contains(invalidRec.Body.String(), "invalid_tenant") {
		t.Fatalf("invalid tenant status=%d body=%s", invalidRec.Code, invalidRec.Body.String())
	}

	missingSource := httptest.NewRequest(http.MethodPost, "/api/v1/tenants",
		strings.NewReader(`{"name":"Tenant","code":"tenant","data_source_id":"missing"}`))
	missingRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(missingRec, missingSource)
	if missingRec.Code != http.StatusNotFound || !strings.Contains(missingRec.Body.String(), "data_source_not_found") {
		t.Fatalf("missing data source status=%d body=%s", missingRec.Code, missingRec.Body.String())
	}
}
