package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
		ErrorCode string `json:"error_code"`
}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.TenantID != item.ID || result.Status != "healthy" || result.Message != "connection successful" || result.ErrorCode != "none" {
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

func TestTenantHealthSettingsAndHistoryAPI(t *testing.T) {
	store, err := adminstore.Open(context.Background(), ":memory:", apiTestKey)
	if err != nil { t.Fatal(err) }
	defer store.Close()
	source, err := store.Create(context.Background(), adminstore.DataSourceInput{Name:"api-health", Driver:"sqlite", DSN:":memory:"})
	if err != nil { t.Fatal(err) }
	item, err := store.CreateTenant(context.Background(), adminstore.TenantInput{Name:"API tenant", Code:"api-tenant", DataSourceID:source.ID})
	if err != nil { t.Fatal(err) }
	if err := store.RecordTenantConnectionHealth(context.Background(), adminstore.TenantConnectionHealth{
		TenantID:item.ID, Status:"healthy", CheckedAt:item.CreatedAt, DurationMS:2, Message:"connection successful",
	}); err != nil { t.Fatal(err) }
	server := newTestServerWithStore(store)
	defer server.Close()

	get := httptest.NewRecorder()
	server.Handler().ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/v1/tenant-health/settings", nil))
	if get.Code != http.StatusOK { t.Fatalf("settings status=%d body=%s", get.Code, get.Body.String()) }

	body := `{"enabled":true,"interval_seconds":60,"timeout_seconds":10,"history_retention_days":14}`
	putReq := httptest.NewRequest(http.MethodPut, "/api/v1/tenant-health/settings", strings.NewReader(body))
	putReq.Header.Set("Content-Type", "application/json")
	put := httptest.NewRecorder()
	server.Handler().ServeHTTP(put, putReq)
	if put.Code != http.StatusOK { t.Fatalf("update settings status=%d body=%s", put.Code, put.Body.String()) }
	if server.healthScheduler == nil { t.Fatal("health scheduler was not initialized") }

	history := httptest.NewRecorder()
	server.Handler().ServeHTTP(history, httptest.NewRequest(http.MethodGet, "/api/v1/tenant-health/history?tenant_id="+item.ID+"&source=manual&limit=10", nil))
	if history.Code != http.StatusOK { t.Fatalf("history status=%d body=%s", history.Code, history.Body.String()) }
	var page adminstore.TenantHealthHistoryPage
	if err := json.Unmarshal(history.Body.Bytes(), &page); err != nil { t.Fatal(err) }
	if len(page.Items) != 1 || page.Items[0].Source != "manual" { t.Fatalf("unexpected history: %+v", page) }

	invalid := httptest.NewRecorder()
	server.Handler().ServeHTTP(invalid, httptest.NewRequest(http.MethodGet, "/api/v1/tenant-health/history?limit=101", nil))
	if invalid.Code != http.StatusBadRequest { t.Fatalf("invalid limit status=%d", invalid.Code) }
}

func TestTenantHealthSchedulerRunsAPIAndScheduledLifecycle(t *testing.T) {
	store, err := adminstore.Open(context.Background(), ":memory:", apiTestKey)
	if err != nil { t.Fatal(err) }
	defer store.Close()
	server := newTestServerWithStore(store)
	defer server.Close()

	if err := server.runScheduledTenantHealthChecks(context.Background(), adminstore.TenantHealthSettings{
		Enabled: true, IntervalSeconds: 300, TimeoutSeconds: 5, HistoryRetentionDays: 30,
	}); err != nil {
		t.Fatalf("run scheduled tenant health checks: %v", err)
	}
	page, err := store.ListTenantHealthSchedulerRuns(context.Background(), adminstore.TenantHealthSchedulerRunFilter{})
	if err != nil { t.Fatal(err) }
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].Status != "succeeded" || page.Items[0].FinishedAt == nil {
		t.Fatalf("scheduled run was not finalized: %+v", page)
	}

	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/tenant-health/runs?status=succeeded&limit=10&offset=0", nil))
	if rec.Code != http.StatusOK { t.Fatalf("runs API status=%d body=%s", rec.Code, rec.Body.String()) }
	var response adminstore.TenantHealthSchedulerRunPage
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil { t.Fatal(err) }
	if response.Total != 1 || len(response.Items) != 1 || response.Items[0].ID != page.Items[0].ID {
		t.Fatalf("unexpected runs API response: %+v", response)
	}
	if response.Summary.Total != 1 || response.Summary.Succeeded != 1 || response.Summary.AverageDurationMS < 0 {
		t.Fatalf("unexpected runs API summary: %+v", response.Summary)
	}

	invalid := httptest.NewRecorder()
	server.Handler().ServeHTTP(invalid, httptest.NewRequest(http.MethodGet, "/api/v1/tenant-health/runs?status=unknown", nil))
	if invalid.Code != http.StatusBadRequest { t.Fatalf("invalid run filter status=%d body=%s", invalid.Code, invalid.Body.String()) }
}

func TestTenantHealthSettingsRBAC(t *testing.T) {
	if !authorizeAdminRole("viewer", http.MethodGet, "/api/v1/tenant-health/settings") {
		t.Fatal("viewer should read health settings")
	}
	if authorizeAdminRole("viewer", http.MethodPut, "/api/v1/tenant-health/settings") {
		t.Fatal("viewer must not update health settings")
	}
	if !authorizeAdminRole("administrator", http.MethodPut, "/api/v1/tenant-health/settings") {
		t.Fatal("administrator should update health settings")
	}
}


func TestRunTenantHealthChecksConcurrentlyCollectsErrorsAndContinues(t *testing.T) {
	wantErr := errors.New("connection metadata unavailable")
	var checked atomic.Int32
	err := runTenantHealthChecksConcurrently(context.Background(), []string{"tenant-a", "tenant-b", "tenant-c"}, func(_ context.Context, tenantID string) error {
		checked.Add(1)
		if tenantID == "tenant-b" {
			return wantErr
		}
		return nil
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected tenant error to be returned, got %v", err)
	}
	if got := checked.Load(); got != 3 {
		t.Fatalf("checked tenants=%d, want all 3 tenants checked", got)
	}
}

func TestRunTenantHealthChecksConcurrentlyHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var checked atomic.Int32
	err := runTenantHealthChecksConcurrently(ctx, []string{"tenant-a", "tenant-b"}, func(context.Context, string) error {
		checked.Add(1)
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	if got := checked.Load(); got != 0 {
		t.Fatalf("checked tenants after cancellation=%d, want 0", got)
	}
}


func TestTenantHealthEndpointsRejectAnonymousRequests(t *testing.T) {
	store, err := adminstore.Open(context.Background(), ":memory:", apiTestKey)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	server := NewServerWithStore(store)
	defer server.Close()

	cases := []struct {
		method string
		path   string
		body   string
	}{
		{method: http.MethodGet, path: "/api/v1/tenant-health"},
		{method: http.MethodGet, path: "/api/v1/tenant-health/settings"},
		{method: http.MethodGet, path: "/api/v1/tenant-health/history"},
		{method: http.MethodGet, path: "/api/v1/tenant-health/runs"},
		{method: http.MethodGet, path: "/api/v1/tenant-health/summary"},
		{method: http.MethodGet, path: "/api/v1/tenant-health/trends"},
		{method: http.MethodPut, path: "/api/v1/tenant-health/settings", body: `{"enabled":true,"interval_seconds":300,"timeout_seconds":5,"history_retention_days":30}`},
	}

	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		if tc.body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s status=%d body=%s, want %d", tc.method, tc.path, rec.Code, rec.Body.String(), http.StatusUnauthorized)
		}
	}
}


func TestTenantHealthCheckCancellationDoesNotPersistResult(t *testing.T) {
	store, err := adminstore.Open(context.Background(), ":memory:", apiTestKey)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	source, err := store.Create(context.Background(), adminstore.DataSourceInput{
		Name: "canceled-health-check", Driver: "sqlite", DSN: ":memory:",
	})
	if err != nil {
		t.Fatal(err)
	}
	item, err := store.CreateTenant(context.Background(), adminstore.TenantInput{
		Name: "Canceled tenant", Code: "canceled-tenant", DataSourceID: source.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := newTestServerWithStore(store)
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := server.runTenantHealthCheckWithOptions(ctx, item.ID, time.Second, "scheduled"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled health check error=%v, want context.Canceled", err)
	}

	history, err := store.ListTenantConnectionHealthHistory(context.Background(), adminstore.TenantHealthHistoryFilter{
		TenantID: item.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Items) != 0 {
		t.Fatalf("canceled health check persisted %d history rows, want 0", len(history.Items))
	}
	latest, err := store.ListTenantConnectionHealth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(latest) != 0 {
		t.Fatalf("canceled health check changed latest health state: %+v", latest)
	}
}


func TestTenantHealthSchedulerSnapshotAPI(t *testing.T) {
	store, err := adminstore.Open(context.Background(), ":memory:", apiTestKey)
	if err != nil { t.Fatal(err) }
	defer store.Close()
	server := newTestServerWithStore(store)
	defer server.Close()
	if server.healthScheduler == nil { t.Fatal("health scheduler was not initialized") }
	// Wait for the scheduler's initial settings load before overriding its snapshot.
	deadline := time.Now().Add(time.Second)
	for server.healthScheduler.Snapshot().IntervalSeconds != 300 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := server.healthScheduler.Snapshot().IntervalSeconds; got != 300 {
		t.Fatalf("scheduler did not finish initial settings load, interval=%d", got)
	}
	server.healthScheduler.updateSettings(adminstore.TenantHealthSettings{Enabled: true, IntervalSeconds: 90})
	next := time.Date(2026, 11, 8, 1, 2, 3, 0, time.UTC)
	server.healthScheduler.setNextRun(&next)
	started := time.Date(2026, 11, 8, 1, 0, 0, 0, time.UTC)
	server.healthScheduler.snapshotMu.Lock()
	server.healthScheduler.snapshot.TotalRuns = 3
	server.healthScheduler.snapshot.SuccessCount = 2
	server.healthScheduler.snapshot.FailureCount = 1
	server.healthScheduler.snapshot.LastStartedAt = &started
	server.healthScheduler.snapshot.LastError = "巡检执行失败"
	server.healthScheduler.snapshotMu.Unlock()
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/tenant-health/scheduler", nil))
	if rec.Code != http.StatusOK { t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String()) }
	var got tenantHealthSchedulerSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil { t.Fatal(err) }
	if !got.Enabled || got.IntervalSeconds != 90 || got.TotalRuns != 3 || got.SuccessCount != 2 || got.FailureCount != 1 {
		t.Fatalf("unexpected scheduler snapshot: %+v", got)
	}
	if got.LastError != "巡检执行失败" || got.NextRunAt == nil || !got.NextRunAt.Equal(next) {
		t.Fatalf("unexpected scheduler details: %+v", got)
	}
	if got.LeaseStatus != "expired_or_unclaimed" {
		t.Fatalf("unexpected scheduler lease status: %+v", got)
	}
}


func TestTenantHealthSummaryEndpointReturnsCurrentAndRecentCounts(t *testing.T) {
	store, err := adminstore.Open(context.Background(), ":memory:", apiTestKey)
	if err != nil { t.Fatal(err) }
	defer store.Close()
	source, err := store.Create(context.Background(), adminstore.DataSourceInput{Name:"summary", Driver:"sqlite", DSN:":memory:"})
	if err != nil { t.Fatal(err) }
	healthy, err := store.CreateTenant(context.Background(), adminstore.TenantInput{Name:"Healthy", Code:"summary-healthy", DataSourceID:source.ID})
	if err != nil { t.Fatal(err) }
	unhealthy, err := store.CreateTenant(context.Background(), adminstore.TenantInput{Name:"Unhealthy", Code:"summary-unhealthy", DataSourceID:source.ID})
	if err != nil { t.Fatal(err) }
	_, err = store.CreateTenant(context.Background(), adminstore.TenantInput{Name:"Unchecked", Code:"summary-unchecked", DataSourceID:source.ID})
	if err != nil { t.Fatal(err) }
	now := time.Now().UTC()
	for _, item := range []adminstore.TenantConnectionHealth{
		{TenantID:healthy.ID, Status:"healthy", CheckedAt:now.Add(-time.Hour), Message:"connection successful"},
		{TenantID:unhealthy.ID, Status:"unhealthy", CheckedAt:now.Add(-2*time.Hour), Message:"connection failed"},
	} {
		if err := store.RecordTenantConnectionHealthWithSource(context.Background(), item, "manual"); err != nil { t.Fatal(err) }
	}
	// An older event is excluded from the rolling 24-hour window.
	if err := store.RecordTenantConnectionHealthWithSource(context.Background(), adminstore.TenantConnectionHealth{
		TenantID:healthy.ID, Status:"healthy", CheckedAt:now.Add(-25*time.Hour), Message:"connection successful",
	}, "scheduled"); err != nil { t.Fatal(err) }

	server := newTestServerWithStore(store)
	defer server.Close()
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/tenant-health/summary", nil))
	if rec.Code != http.StatusOK { t.Fatalf("summary status=%d body=%s", rec.Code, rec.Body.String()) }
	var got adminstore.TenantHealthSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil { t.Fatal(err) }
	if got.TenantCount != 3 || got.CheckedTenantCount != 2 || got.HealthyTenantCount != 1 || got.UnhealthyTenantCount != 1 || got.UncheckedTenantCount != 1 {
		t.Fatalf("unexpected latest tenant counts: %+v", got)
	}
	if got.ChecksLast24Hours != 2 || got.HealthyChecksLast24Hours != 1 || got.UnhealthyChecksLast24Hours != 1 || got.SuccessRateLast24Hours != 50 {
		t.Fatalf("unexpected 24-hour summary: %+v", got)
	}
}


func TestTenantHealthTrendsAPIValidatesRangeAndReturnsBuckets(t *testing.T) {
	store, err := adminstore.Open(context.Background(), ":memory:", apiTestKey)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server := newTestServerWithStore(store)
	defer server.Close()

	for _, tc := range []struct {
		path string
		want int
		buckets int
	}{
		{path: "/api/v1/tenant-health/trends", want: http.StatusOK, buckets: 24},
		{path: "/api/v1/tenant-health/trends?range=24h", want: http.StatusOK, buckets: 24},
		{path: "/api/v1/tenant-health/trends?range=7d", want: http.StatusOK, buckets: 7},
		{path: "/api/v1/tenant-health/trends?range=30d", want: http.StatusBadRequest},
	} {
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Code != tc.want {
			t.Errorf("%s status=%d body=%s, want %d", tc.path, rec.Code, rec.Body.String(), tc.want)
			continue
		}
		if tc.want == http.StatusOK {
			var got adminstore.TenantHealthTrends
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Errorf("%s response decode failed: %v", tc.path, err)
				continue
			}
			if len(got.Buckets) != tc.buckets {
				t.Errorf("%s buckets=%d, want %d", tc.path, len(got.Buckets), tc.buckets)
			}
		}
	}
}

func TestTenantHealthSchedulerAPIReportsSharedLeaseState(t *testing.T) {
	store, err := adminstore.Open(context.Background(), ":memory:", apiTestKey)
	if err != nil { t.Fatal(err) }
	defer store.Close()
	server := newTestServerWithStore(store)
	defer server.Close()
	owner := server.tenantHealthLeaseOwnerID
	if owner == "" { t.Fatal("scheduler owner ID was not configured") }

	acquired, _, err := store.AcquireTenantHealthSchedulerLease(context.Background(), owner, time.Now().UTC(), 30*time.Second)
	if err != nil || !acquired { t.Fatalf("acquire local lease: acquired=%v err=%v", acquired, err) }
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/tenant-health/scheduler", nil))
	if rec.Code != http.StatusOK { t.Fatalf("local lease status=%d body=%s", rec.Code, rec.Body.String()) }
	var local map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &local); err != nil { t.Fatal(err) }
	if local["lease_status"] != "held_by_this_instance" || local["lease_generation"] != float64(1) {
		t.Fatalf("unexpected local lease status: %+v", local)
	}
	if _, leaked := local["lease_owner_id"]; leaked { t.Fatal("scheduler API exposed lease owner ID") }

	if err := store.ReleaseTenantHealthSchedulerLease(context.Background(), owner); err != nil { t.Fatal(err) }
	acquired, _, err = store.AcquireTenantHealthSchedulerLease(context.Background(), "another-instance", time.Now().UTC(), 30*time.Second)
	if err != nil || !acquired { t.Fatalf("acquire other lease: acquired=%v err=%v", acquired, err) }
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/tenant-health/scheduler", nil))
	if rec.Code != http.StatusOK { t.Fatalf("other lease status=%d body=%s", rec.Code, rec.Body.String()) }
	local = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &local); err != nil { t.Fatal(err) }
	if local["lease_status"] != "held_by_other_instance" { t.Fatalf("unexpected other lease status: %+v", local) }
}
