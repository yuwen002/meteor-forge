package adminapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/yuwen002/meteor-forge/internal/adminstore"
)

func TestOperationAuditEndpointFiltersAndPaginates(t *testing.T) {
	ctx := context.Background()
	store, err := adminstore.Open(ctx, ":memory:", apiTestKey)
	if err != nil { t.Fatal(err) }
	defer store.Close()

	migrationSource, err := store.Create(ctx, adminstore.DataSourceInput{Name:"audit migration target", Driver:"sqlite", DSN:":memory:"})
	if err != nil { t.Fatal(err) }
	tenant, err := store.CreateTenant(ctx, adminstore.TenantInput{Name:"audit tenant", Code:"audit-tenant", DataSourceID:migrationSource.ID})
	if err != nil { t.Fatal(err) }
	migrationTask, err := store.CreateMigrationTask(ctx, adminstore.MigrationTask{
		ID: "audit-task-1", DataSourceID: migrationSource.ID, TenantID:tenant.ID, Action: "up",
	})
	if err != nil { t.Fatal(err) }
	started := time.Now().UTC()
	if err := store.UpdateMigrationTask(ctx, migrationTask.ID, "running", "migration started", &started, nil); err != nil { t.Fatal(err) }
	finished := time.Now().UTC()
	if err := store.UpdateMigrationTask(ctx, migrationTask.ID, "succeeded", "migration completed", nil, &finished); err != nil { t.Fatal(err) }

	source, err := store.Create(ctx, adminstore.DataSourceInput{Name:"audit shard target", Driver:"sqlite", DSN:":memory:"})
	if err != nil { t.Fatal(err) }
	rule, err := store.CreateShardRule(ctx, adminstore.ShardRuleInput{DataSourceID:source.ID, BaseTable:"orders", ShardType:"month"})
	if err != nil { t.Fatal(err) }
	shardTask, err := store.CreateShardDDLTask(ctx, adminstore.ShardDDLTask{
		ShardRuleID:rule.ID, DataSourceID:source.ID, Action:"clone",
		SourceTable:"orders", TargetTable:"orders_202611", RequestSource:"127.0.0.2",
	})
	if err != nil { t.Fatal(err) }
	shardStarted := time.Now().UTC()
	if err := store.UpdateShardDDLTask(ctx, shardTask.ID, "running", "DDL started", &shardStarted, nil); err != nil { t.Fatal(err) }
	shardFinished := time.Now().UTC()
	if err := store.UpdateShardDDLTask(ctx, shardTask.ID, "succeeded", "DDL completed", nil, &shardFinished); err != nil { t.Fatal(err) }

	server := NewServerWithStore(store)
	defer server.Close()
	request := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, req)
		return rec
	}

	type auditResponse struct {
		Items []adminstore.OperationAuditEvent `json:"items"`
		Total int `json:"total"`
		Limit int `json:"limit"`
		Offset int `json:"offset"`
	}
	all := request("/api/v1/audit-events")
	if all.Code != http.StatusOK { t.Fatalf("all audit status=%d body=%s", all.Code, all.Body.String()) }
	var allResult auditResponse
	if err := json.Unmarshal(all.Body.Bytes(), &allResult); err != nil { t.Fatal(err) }
	if allResult.Total != 6 || len(allResult.Items) != 6 { t.Fatalf("combined audit total/items = %d/%d, want 6/6", allResult.Total, len(allResult.Items)) }
	foundShardSource := false
	for _, event := range allResult.Items {
		if event.Kind == "shard_ddl" && event.TaskID == shardTask.ID && event.ToStatus == "succeeded" {
			foundShardSource = event.RequestSource == "127.0.0.2"
		}
	}
	if !foundShardSource { t.Fatalf("combined audit did not preserve shard DDL request source: %+v", allResult.Items) }

	shardOnly := request("/api/v1/audit-events?kind=shard_ddl")
	if shardOnly.Code != http.StatusOK { t.Fatalf("shard audit status=%d body=%s", shardOnly.Code, shardOnly.Body.String()) }
	var shardResult auditResponse
	if err := json.Unmarshal(shardOnly.Body.Bytes(), &shardResult); err != nil { t.Fatal(err) }
	if shardResult.Total != 3 || len(shardResult.Items) != 3 { t.Fatalf("shard audit total/items = %d/%d, want 3/3", shardResult.Total, len(shardResult.Items)) }
	for _, event := range shardResult.Items {
		if event.Kind != "shard_ddl" { t.Errorf("kind filter returned %q", event.Kind) }
	}
	for _, tc := range []struct{ path string; want int }{
		{"/api/v1/audit-events?task_id="+shardTask.ID, 3},
		{"/api/v1/audit-events?data_source_id="+source.ID, 3},
		{"/api/v1/audit-events?tenant_id="+tenant.ID, 3},
		{"/api/v1/audit-events?shard_rule_id="+rule.ID, 3},
	} {
		response := request(tc.path)
		if response.Code != http.StatusOK { t.Fatalf("%s status=%d body=%s", tc.path, response.Code, response.Body.String()) }
		var result auditResponse
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil { t.Fatal(err) }
		if result.Total != tc.want { t.Errorf("%s total=%d, want %d", tc.path, result.Total, tc.want) }
	}
	from := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	to := time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano)
	timeFiltered := request("/api/v1/audit-events?from="+url.QueryEscape(from)+"&to="+url.QueryEscape(to))
	if timeFiltered.Code != http.StatusOK { t.Fatalf("time range status=%d body=%s", timeFiltered.Code, timeFiltered.Body.String()) }
	var timeResult auditResponse
	if err := json.Unmarshal(timeFiltered.Body.Bytes(), &timeResult); err != nil { t.Fatal(err) }
	if timeResult.Total != 6 { t.Fatalf("time range total=%d, want 6", timeResult.Total) }

	filtered := request("/api/v1/audit-events?kind=migration&status=succeeded&limit=1&offset=0")
	if filtered.Code != http.StatusOK { t.Fatalf("filtered audit status=%d body=%s", filtered.Code, filtered.Body.String()) }
	var filteredResult auditResponse
	if err := json.Unmarshal(filtered.Body.Bytes(), &filteredResult); err != nil { t.Fatal(err) }
	if filteredResult.Total != 1 || len(filteredResult.Items) != 1 || filteredResult.Items[0].TaskID != migrationTask.ID || filteredResult.Items[0].Kind != "migration" {
		t.Fatalf("unexpected filtered migration audit: %+v", filteredResult)
	}

	paged := request("/api/v1/audit-events?limit=1&offset=1")
	if paged.Code != http.StatusOK { t.Fatalf("paged audit status=%d body=%s", paged.Code, paged.Body.String()) }
	var pagedResult auditResponse
	if err := json.Unmarshal(paged.Body.Bytes(), &pagedResult); err != nil { t.Fatal(err) }
	if pagedResult.Total != 6 || len(pagedResult.Items) != 1 || pagedResult.Limit != 1 || pagedResult.Offset != 1 {
		t.Fatalf("unexpected paged audit response: %+v", pagedResult)
	}

	for _, tc := range []struct{ path string; want int }{
		{"/api/v1/audit-events?kind=unknown", http.StatusBadRequest},
		{"/api/v1/audit-events?status=unknown", http.StatusBadRequest},
		{"/api/v1/audit-events?limit=201", http.StatusBadRequest},
		{"/api/v1/audit-events?offset=-1", http.StatusBadRequest},
		{"/api/v1/audit-events?from=not-a-time", http.StatusBadRequest},
		{"/api/v1/audit-events?from=2026-11-05T12:00:00Z&to=2026-11-04T12:00:00Z", http.StatusBadRequest},
	} {
		response := request(tc.path)
		if response.Code != tc.want || !strings.Contains(response.Body.String(), "error") {
			t.Errorf("%s status=%d body=%s, want %d", tc.path, response.Code, response.Body.String(), tc.want)
		}
	}
}
