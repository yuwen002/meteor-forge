package adminapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yuwen002/meteor-forge/internal/adminstore"
)

func TestOperationAuditEndpointFiltersAndPaginates(t *testing.T) {
	store, err := adminstore.Open(context.Background(), ":memory:", apiTestKey)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	task, err := store.CreateMigrationTask(context.Background(), adminstore.MigrationTask{
		ID: "audit-task-1", DataSourceID: "audit-source", Action: "up",
	})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC()
	if err := store.UpdateMigrationTask(context.Background(), task.ID, "running", "migration started", &started, nil); err != nil {
		t.Fatal(err)
	}
	finished := time.Now().UTC()
	if err := store.UpdateMigrationTask(context.Background(), task.ID, "succeeded", "migration completed", nil, &finished); err != nil {
		t.Fatal(err)
	}

	server := NewServerWithStore(store)
	defer server.Close()

	request := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, req)
		return rec
	}
	filtered := request("/api/v1/audit-events?kind=migration&status=succeeded&limit=1&offset=0")
	if filtered.Code != http.StatusOK {
		t.Fatalf("filtered audit status=%d body=%s", filtered.Code, filtered.Body.String())
	}
	body := filtered.Body.String()
	for _, expected := range []string{task.ID, `"total":1`, `"kind":"migration"`, `"to_status":"succeeded"`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("audit response missing %q: %s", expected, body)
		}
	}
	invalid := request("/api/v1/audit-events?kind=unknown")
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid kind status=%d body=%s", invalid.Code, invalid.Body.String())
	}
	paged := request("/api/v1/audit-events?limit=1&offset=1")
	if paged.Code != http.StatusOK || !strings.Contains(paged.Body.String(), `"total":3`) {
		t.Fatalf("paged audit status=%d body=%s", paged.Code, paged.Body.String())
	}
}
