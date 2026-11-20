package adminapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yuwen002/meteor-forge/internal/adminstore"
)

func TestTenantHealthRoutesRequireAdministratorForChecks(t *testing.T) {
	for _, tc := range []struct{method,path string; want bool}{
		{http.MethodGet,"/api/v1/tenant-health",true},
		{http.MethodPost,"/api/v1/tenants/t1/health-check",false},
		{http.MethodPost,"/api/v1/tenant-health-checks",false},
	} {
		if got:=authorizeAdminRole("viewer",tc.method,tc.path);got!=tc.want {
			t.Errorf("%s %s viewer permission=%v, want %v",tc.method,tc.path,got,tc.want)
		}
	}
}

func TestTenantHealthBatchRejectsInvalidAndDuplicateIDs(t *testing.T) {
	store,err:=adminstore.Open(context.Background(),":memory:",apiTestKey)
	if err!=nil{t.Fatal(err)}
	defer store.Close()
	server:=newTestServerWithStore(store)
	defer server.Close()
	for _,body:=range []string{
		`{"tenant_ids":[]}`,
		`{"tenant_ids":["t1","t1"]}`,
		`{"tenant_ids":[""]}`,
		`{"tenant_ids":["t1"],"unexpected":true}`,
	} {
		req:=httptest.NewRequest(http.MethodPost,"/api/v1/tenant-health-checks",strings.NewReader(body))
		req.Header.Set("Content-Type","application/json")
		rec:=httptest.NewRecorder()
		server.Handler().ServeHTTP(rec,req)
		if rec.Code!=http.StatusBadRequest { t.Errorf("body=%s status=%d body=%s",body,rec.Code,rec.Body.String()) }
	}
}

func TestTenantHealthListReturnsPersistedMetadata(t *testing.T) {
	store,err:=adminstore.Open(context.Background(),":memory:",apiTestKey)
	if err!=nil{t.Fatal(err)}
	defer store.Close()
	source,err:=store.Create(context.Background(),adminstore.DataSourceInput{Name:"health",Driver:"sqlite",DSN:":memory:"})
	if err!=nil{t.Fatal(err)}
	item,err:=store.CreateTenant(context.Background(),adminstore.TenantInput{Name:"Tenant",Code:"tenant",DataSourceID:source.ID})
	if err!=nil{t.Fatal(err)}
	if err:=store.RecordTenantConnectionHealth(context.Background(),adminstore.TenantConnectionHealth{
		TenantID:item.ID,Status:"healthy",CheckedAt:item.CreatedAt,DurationMS:5,Message:"connection successful",
	});err!=nil{t.Fatal(err)}
	server:=newTestServerWithStore(store)
	defer server.Close()
	req:=httptest.NewRequest(http.MethodGet,"/api/v1/tenant-health",nil)
	rec:=httptest.NewRecorder()
	server.Handler().ServeHTTP(rec,req)
	if rec.Code!=http.StatusOK {t.Fatalf("status=%d body=%s",rec.Code,rec.Body.String())}
	var payload struct{Items []adminstore.TenantConnectionHealth `json:"items"`}
	if err:=json.Unmarshal(rec.Body.Bytes(),&payload);err!=nil{t.Fatal(err)}
	if len(payload.Items)!=1 || payload.Items[0].TenantID!=item.ID || payload.Items[0].Status!="healthy" {
		t.Fatalf("unexpected health payload: %+v",payload.Items)
	}
}

func TestTenantHealthCheckPersistsSingleTenantResult(t *testing.T) {
	store, err := adminstore.Open(context.Background(), ":memory:", apiTestKey)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	source, err := store.Create(context.Background(), adminstore.DataSourceInput{
		Name: "health-check-source", Driver: "sqlite", DSN: ":memory:",
	})
	if err != nil {
		t.Fatal(err)
	}
	item, err := store.CreateTenant(context.Background(), adminstore.TenantInput{
		Name: "Health tenant", Code: "health-tenant", DataSourceID: source.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := newTestServerWithStore(store)
	defer server.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/"+item.ID+"/health-check", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var result struct {
		TenantID string `json:"tenant_id"`
		Status string `json:"status"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.TenantID != item.ID || result.Status != "healthy" || result.Message != "connection successful" {
		t.Fatalf("unexpected health check result: %+v", result)
	}
	persisted, err := store.ListTenantConnectionHealth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted) != 1 || persisted[0].TenantID != item.ID || persisted[0].Status != "healthy" {
		t.Fatalf("health result was not persisted: %+v", persisted)
	}
}
