package adminapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yuwen002/meteor-forge/internal/adminstore"
)

func TestHealthEndpoint(t *testing.T) {
	server := NewServer()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	recorder := httptest.NewRecorder()

	server.Handler().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := recorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}

	var body struct {
		Status  string `json:"status"`
		Service string `json:"service"`
		API     string `json:"api"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Status != "ok" || body.Service != "meteor-forge-admin" || body.API != "v1" {
		t.Fatalf("unexpected health response: %+v", body)
	}
}

func TestUnknownRouteReturnsNotFound(t *testing.T) {
	server := NewServer()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/unknown", nil)
	recorder := httptest.NewRecorder()

	server.Handler().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
}

func TestManagementAPIRequiresAuthenticationBeforeBootstrap(t *testing.T) {
 store,err:=adminstore.Open(context.Background(),":memory:",apiTestKey)
 if err!=nil{t.Fatal(err)}
 defer store.Close()
 server:=NewServerWithStore(store)
 defer server.Close()
 health:=httptest.NewRequest(http.MethodGet,"http://localhost/api/v1/health",nil)
 healthRec:=httptest.NewRecorder()
 server.Handler().ServeHTTP(healthRec,health)
 if healthRec.Code!=http.StatusOK{t.Fatalf("health status=%d body=%s",healthRec.Code,healthRec.Body.String())}
 request:=httptest.NewRequest(http.MethodGet,"http://localhost/api/v1/datasources",nil)
 rec:=httptest.NewRecorder()
 server.Handler().ServeHTTP(rec,request)
 if rec.Code!=http.StatusUnauthorized{t.Fatalf("unbootstrapped management API status=%d body=%s",rec.Code,rec.Body.String())}
}
