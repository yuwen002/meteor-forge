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

func TestTenantMigrationBatchIsolatesFailuresAndPersistsSummary(t *testing.T) {
	dir := t.TempDir()
	writeMigrationPair(t, dir, "000001_create_batch_items",
		"CREATE TABLE batch_items (id INTEGER PRIMARY KEY)",
		"DROP TABLE batch_items",
	)
	store, err := adminstore.Open(context.Background(), ":memory:", apiTestKey)
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = store.Close() })

	sharedSource, err := store.Create(context.Background(), adminstore.DataSourceInput{
		Name: "batch shared source", Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "shared.db"),
	})
	if err != nil { t.Fatal(err) }
	brokenSource, err := store.Create(context.Background(), adminstore.DataSourceInput{
		Name: "batch broken source", Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "missing-parent", "broken.db"),
	})
	if err != nil { t.Fatal(err) }

	tenants := make([]adminstore.Tenant, 0, 3)
	for i, sourceID := range []string{sharedSource.ID, sharedSource.ID, brokenSource.ID} {
		item, createErr := store.CreateTenant(context.Background(), adminstore.TenantInput{
			Name: "Batch tenant " + string(rune('A'+i)),
			Code: "batch-tenant-" + string(rune('a'+i)),
			DataSourceID: sourceID, Status: "active",
		})
		if createErr != nil { t.Fatal(createErr) }
		tenants = append(tenants, item)
	}

	server := newTestServerWithMigrationDir(store, dir)
	t.Cleanup(func() { _ = server.Close() })
	ids := []string{tenants[0].ID, tenants[1].ID, tenants[2].ID}
	body, _ := json.Marshal(map[string]any{"tenant_ids": ids})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenant-migration-batches", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("submit batch status=%d body=%s", rec.Code, rec.Body.String())
	}
	var submitted tenantMigrationBatchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &submitted); err != nil { t.Fatal(err) }
	if submitted.Total != 3 || submitted.Queued != 3 || submitted.Failed != 0 || len(submitted.Tasks) != 3 {
		t.Fatalf("unexpected batch submission summary: %+v", submitted)
	}
	for _, task := range submitted.Tasks {
		if task.BatchID != submitted.ID { t.Fatalf("task batch ID=%q want %q", task.BatchID, submitted.ID) }
		if task.Action != "up" { t.Fatalf("batch action=%q want up", task.Action) }
	}

	for _, task := range submitted.Tasks {
		want := "succeeded"
		if task.TenantID == tenants[2].ID { want = "failed" }
		waitForTaskStatus(t, store, task.ID, want)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/tenant-migration-batches/"+submitted.ID, nil)
	getRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("get batch status=%d body=%s", getRec.Code, getRec.Body.String())
	}
	var summary tenantMigrationBatchResponse
	if err := json.Unmarshal(getRec.Body.Bytes(), &summary); err != nil { t.Fatal(err) }
	if summary.Total != 3 || summary.Succeeded != 2 || summary.Failed != 1 {
		t.Fatalf("unexpected completed batch summary: %+v", summary)
	}
}

func TestTenantMigrationBatchRejectsDuplicateAndSuspendedTenants(t *testing.T) {
	dir := t.TempDir()
	store, err := adminstore.Open(context.Background(), ":memory:", apiTestKey)
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = store.Close() })
	source, err := store.Create(context.Background(), adminstore.DataSourceInput{
		Name: "batch validation source", Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "validation.db"),
	})
	if err != nil { t.Fatal(err) }
	active, err := store.CreateTenant(context.Background(), adminstore.TenantInput{
		Name: "Active", Code: "batch-active", DataSourceID: source.ID, Status: "active",
	})
	if err != nil { t.Fatal(err) }
	suspended, err := store.CreateTenant(context.Background(), adminstore.TenantInput{
		Name: "Suspended", Code: "batch-suspended", DataSourceID: source.ID, Status: "suspended",
	})
	if err != nil { t.Fatal(err) }

	server := newTestServerWithMigrationDir(store, dir)
	t.Cleanup(func() { _ = server.Close() })
	for _, tc := range []struct{ ids []string; wantCode string }{
		{[]string{active.ID, active.ID}, "duplicate_tenant_id"},
		{[]string{active.ID, suspended.ID}, "tenant_suspended"},
	} {
		body, _ := json.Marshal(map[string]any{"tenant_ids": tc.ids})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/tenant-migration-batches", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, req)
		if rec.Code < 400 || !strings.Contains(rec.Body.String(), tc.wantCode) {
			t.Fatalf("batch validation response status=%d body=%s want=%s", rec.Code, rec.Body.String(), tc.wantCode)
		}
	}
	tasks, err := store.ListMigrationTasks(context.Background(), 50)
	if err != nil { t.Fatal(err) }
	if len(tasks) != 0 { t.Fatalf("validation created %d tasks; want none", len(tasks)) }
}

func TestTenantMigrationBatchPersistsSubmissionFailuresWithoutChildTasks(t *testing.T) {
	dir := t.TempDir()
	store, err := adminstore.Open(context.Background(), ":memory:", apiTestKey)
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = store.Close() })
	source, err := store.Create(context.Background(), adminstore.DataSourceInput{
		Name: "batch blocked source", Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "blocked.db"),
	})
	if err != nil { t.Fatal(err) }
	tenant, err := store.CreateTenant(context.Background(), adminstore.TenantInput{
		Name: "Blocked tenant", Code: "batch-blocked", DataSourceID: source.ID, Status: "active",
	})
	if err != nil { t.Fatal(err) }
	rule, err := store.CreateShardRule(context.Background(), adminstore.ShardRuleInput{
		DataSourceID: source.ID, BaseTable: "orders", ShardType: "month",
	})
	if err != nil { t.Fatal(err) }

	server := newTestServerWithMigrationDir(store, dir)
	t.Cleanup(func() { _ = server.Close() })
	// Persist an active shard DDL task directly. It holds the database-level
	// mutual-exclusion guard without entering the server's in-memory worker queue.
	if _, err := store.CreateShardDDLTask(context.Background(), adminstore.ShardDDLTask{
		ShardRuleID: rule.ID, DataSourceID: source.ID, Action: "clone",
		SourceTable: "orders_202611", TargetTable: "orders_202612",
	}); err != nil { t.Fatal(err) }

	body, _ := json.Marshal(map[string]any{"tenant_ids": []string{tenant.ID}})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenant-migration-batches", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("submit batch status=%d body=%s", rec.Code, rec.Body.String())
	}
	var submitted tenantMigrationBatchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &submitted); err != nil { t.Fatal(err) }
	if submitted.Total != 1 || submitted.Queued != 0 || submitted.Failed != 1 || submitted.SubmissionFailed != 1 || len(submitted.Tasks) != 0 {
		t.Fatalf("unexpected failed submission summary: %+v", submitted)
	}
	if !submitted.AuditPersisted || len(submitted.AuditEvents) != 2 {
		t.Fatalf("batch submission audit was not persisted: %+v", submitted)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/tenant-migration-batches/"+submitted.ID, nil)
	getRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("get failed-submission batch status=%d body=%s", getRec.Code, getRec.Body.String())
	}
	var summary tenantMigrationBatchResponse
	if err := json.Unmarshal(getRec.Body.Bytes(), &summary); err != nil { t.Fatal(err) }
	if summary.Total != 1 || summary.Failed != 1 || summary.SubmissionFailed != 1 || len(summary.Tasks) != 0 || len(summary.Failures) != 1 {
		t.Fatalf("failed submission batch was not recoverable: %+v", summary)
	}
	if len(summary.AuditEvents) != 2 || summary.Actor != "local-api" {
		t.Fatalf("batch-level audit identity/events missing: %+v", summary)
	}
}
