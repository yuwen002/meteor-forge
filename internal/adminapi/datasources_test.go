package adminapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yuwen002/meteor-forge/internal/adminstore"
)

const apiTestKey = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"

func TestDataSourceAPIEncryptsAndRedactsDSN(t *testing.T) {
	store, err := adminstore.Open(context.Background(), ":memory:", apiTestKey)
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = store.Close() })
	server := NewServerWithStore(store)
	t.Cleanup(func() { _ = server.Close() })

	body := []byte(`{"name":"test-db","driver":"sqlite","dsn":":memory:"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/datasources", bytes.NewReader(body))
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusCreated { t.Fatalf("create status = %d, body=%s", recorder.Code, recorder.Body.String()) }
	if strings.Contains(recorder.Body.String(), ":memory:") || strings.Contains(recorder.Body.String(), "dsn") {
		t.Fatalf("create response leaked DSN: %s", recorder.Body.String())
	}
	var created adminstore.DataSource
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil { t.Fatal(err) }

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/datasources", nil)
	listRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK || strings.Contains(listRec.Body.String(), ":memory:") {
		t.Fatalf("unexpected list response: status=%d body=%s", listRec.Code, listRec.Body.String())
	}
}

func TestDataSourceAPIRejectsUnknownFields(t *testing.T) {
	store, err := adminstore.Open(context.Background(), ":memory:", apiTestKey)
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = store.Close() })
	server := NewServerWithStore(store)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/datasources", strings.NewReader(`{"name":"x","driver":"sqlite","dsn":":memory:","unexpected":true}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest { t.Fatalf("status = %d, want 400", rec.Code) }
}

func TestDataSourceConnectionTestEndpoint(t *testing.T) {
	store, err := adminstore.Open(context.Background(), ":memory:", apiTestKey)
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = store.Close() })
	created, err := store.Create(context.Background(), adminstore.DataSourceInput{
		Name: "local sqlite", Driver: "sqlite", DSN: ":memory:",
	})
	if err != nil { t.Fatal(err) }

	server := NewServerWithStore(store)
	t.Cleanup(func() { _ = server.Close() })
	req := httptest.NewRequest(http.MethodPost, "/api/v1/datasources/"+created.ID+"/test", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK { t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String()) }
	if !strings.Contains(rec.Body.String(), `"success":true`) {
		t.Fatalf("connection test did not succeed: %s", rec.Body.String())
	}
}

func TestDataSourceMutationInvalidatesCachedPool(t *testing.T) {
	store, err := adminstore.Open(context.Background(), ":memory:", apiTestKey)
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = store.Close() })
	created, err := store.Create(context.Background(), adminstore.DataSourceInput{
		Name: "pool target", Driver: "sqlite", DSN: ":memory:",
	})
	if err != nil { t.Fatal(err) }

	server := NewServerWithStore(store)
	t.Cleanup(func() { _ = server.Close() })
	request := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, req)
		return rec
	}

	testPath := "/api/v1/datasources/" + created.ID + "/test"
	if rec := request(http.MethodPost, testPath, ""); rec.Code != http.StatusOK {
		t.Fatalf("initial connection test status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(server.pools.pools) != 1 { t.Fatalf("cached pools = %d, want 1", len(server.pools.pools)) }

	updateBody := `{"name":"pool target updated","driver":"sqlite","dsn":":memory:"}`
	if rec := request(http.MethodPut, "/api/v1/datasources/"+created.ID, updateBody); rec.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(server.pools.pools) != 0 { t.Fatalf("pools after update = %d, want 0", len(server.pools.pools)) }

	if rec := request(http.MethodPost, testPath, ""); rec.Code != http.StatusOK {
		t.Fatalf("connection test after update status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := request(http.MethodDelete, "/api/v1/datasources/"+created.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(server.pools.pools) != 0 { t.Fatalf("pools after delete = %d, want 0", len(server.pools.pools)) }
}
