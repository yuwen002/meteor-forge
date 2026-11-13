package adminstore

import (
	"context"
	"errors"
	"encoding/base64"
	"testing"
	"time"
)

func newTenantForHealthTest(t *testing.T, store *Store, code string) Tenant {
	t.Helper()
	source, err := store.Create(context.Background(), DataSourceInput{Name: "source-" + code, Driver: "sqlite", DSN: ":memory:"})
	if err != nil { t.Fatal(err) }
	tenant, err := store.CreateTenant(context.Background(), TenantInput{Name: code, Code: code, DataSourceID: source.ID})
	if err != nil { t.Fatal(err) }
	return tenant
}

func TestTenantHealthSettingsDefaultsAndValidation(t *testing.T) {
	store, err := Open(context.Background(), ":memory:", testKey)
	if err != nil { t.Fatal(err) }
	defer store.Close()
	got, err := store.GetTenantHealthSettings(context.Background())
	if err != nil { t.Fatal(err) }
	if got.Enabled || got.IntervalSeconds != 300 || got.TimeoutSeconds != 5 || got.HistoryRetentionDays != 30 {
		t.Fatalf("unexpected defaults: %+v", got)
	}
	got.IntervalSeconds = 29
	if _, err := store.UpdateTenantHealthSettings(context.Background(), got); err == nil {
		t.Fatal("expected interval lower-bound validation")
	}
	got.IntervalSeconds, got.TimeoutSeconds, got.HistoryRetentionDays, got.Enabled = 86400, 30, 365, true
	if _, err := store.UpdateTenantHealthSettings(context.Background(), got); err != nil { t.Fatalf("valid upper bounds rejected: %v", err) }
}

func TestTenantHealthErrorCodeIsPersistedAndWhitelisted(t *testing.T) {
	store, err := Open(context.Background(), ":memory:", testKey)
	if err != nil { t.Fatal(err) }
	defer store.Close()
	tenant := newTenantForHealthTest(t, store, "error-code")
	stamp := time.Date(2026, 11, 9, 1, 0, 0, 0, time.UTC)
	if err := store.RecordTenantConnectionHealth(context.Background(), TenantConnectionHealth{
		TenantID: tenant.ID, Status: "unhealthy", CheckedAt: stamp,
		Message: "connection check failed; verify the registered data source", ErrorCode: "authentication_failed",
	}); err != nil { t.Fatal(err) }
	latest, err := store.ListTenantConnectionHealth(context.Background())
	if err != nil { t.Fatal(err) }
	if len(latest) != 1 || latest[0].ErrorCode != "authentication_failed" { t.Fatalf("latest code not persisted: %+v", latest) }
	history, err := store.ListTenantConnectionHealthHistory(context.Background(), TenantHealthHistoryFilter{TenantID: tenant.ID})
	if err != nil { t.Fatal(err) }
	if len(history.Items) != 1 || history.Items[0].ErrorCode != "authentication_failed" { t.Fatalf("history code not persisted: %+v", history) }
	if err := store.RecordTenantConnectionHealth(context.Background(), TenantConnectionHealth{
		TenantID: tenant.ID, Status: "unhealthy", CheckedAt: stamp.Add(time.Second),
		Message: "postgres://root:secret@db", ErrorCode: "driver_raw_error",
	}); err != nil { t.Fatal(err) }
	history, err = store.ListTenantConnectionHealthHistory(context.Background(), TenantHealthHistoryFilter{TenantID: tenant.ID})
	if err != nil { t.Fatal(err) }
	if history.Items[0].ErrorCode != "unknown" || history.Items[0].Message != "connection check failed; verify the registered data source" {
		t.Fatalf("unsafe error metadata not normalized: %+v", history.Items[0])
	}
}

func TestTenantHealthHistoryAndLatestAreAtomicAndPaginated(t *testing.T) {
	store, err := Open(context.Background(), ":memory:", testKey)
	if err != nil { t.Fatal(err) }
	defer store.Close()
	tenant := newTenantForHealthTest(t, store, "history")
	base := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		err := store.RecordTenantConnectionHealthWithSource(context.Background(), TenantConnectionHealth{
			TenantID: tenant.ID, Status: "healthy", CheckedAt: base.Add(time.Duration(i)*time.Second),
			DurationMS: int64(i), Message: "connection successful",
		}, "scheduled")
		if err != nil { t.Fatal(err) }
	}
	page1, err := store.ListTenantConnectionHealthHistory(context.Background(), TenantHealthHistoryFilter{TenantID: tenant.ID, Limit: 2})
	if err != nil { t.Fatal(err) }
	if len(page1.Items) != 2 || page1.NextCursor == "" || page1.Items[0].CheckedAt.Before(page1.Items[1].CheckedAt) {
		t.Fatalf("unexpected first history page: %+v", page1)
	}
	if _, err := base64.RawURLEncoding.DecodeString(page1.NextCursor); err != nil { t.Fatalf("invalid cursor: %v", err) }
	page2, err := store.ListTenantConnectionHealthHistory(context.Background(), TenantHealthHistoryFilter{TenantID: tenant.ID, Limit: 2, Cursor: page1.NextCursor})
	if err != nil { t.Fatal(err) }
	if len(page2.Items) != 1 || page2.Items[0].ID == page1.Items[1].ID { t.Fatalf("unstable next page: %+v", page2) }
	latest, err := store.ListTenantConnectionHealth(context.Background())
	if err != nil { t.Fatal(err) }
	if len(latest) != 1 || !latest[0].CheckedAt.Equal(base.Add(2*time.Second)) { t.Fatalf("latest snapshot mismatch: %+v", latest) }
}

func TestTenantHealthHistorySanitizesAndRetentionIsBounded(t *testing.T) {
	store, err := Open(context.Background(), ":memory:", testKey)
	if err != nil { t.Fatal(err) }
	defer store.Close()
	tenant := newTenantForHealthTest(t, store, "retention")
	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		if err := store.RecordTenantConnectionHealthWithSource(context.Background(), TenantConnectionHealth{
			TenantID: tenant.ID, Status: "unhealthy", CheckedAt: now.Add(time.Duration(i-3)*time.Hour),
			Message: "postgres://user:secret@db",
		}, "manual"); err != nil { t.Fatal(err) }
	}
	page, err := store.ListTenantConnectionHealthHistory(context.Background(), TenantHealthHistoryFilter{TenantID: tenant.ID})
	if err != nil { t.Fatal(err) }
	if len(page.Items) != 3 || page.Items[0].Message != "connection check failed; verify the registered data source" {
		t.Fatalf("history was not normalized: %+v", page)
	}
	deleted, err := store.DeleteExpiredTenantConnectionHealthHistory(context.Background(), now.Add(-time.Hour), 1)
	if err != nil { t.Fatal(err) }
	if deleted != 1 { t.Fatalf("deleted=%d, want bounded delete of 1", deleted) }
	page, err = store.ListTenantConnectionHealthHistory(context.Background(), TenantHealthHistoryFilter{TenantID: tenant.ID})
	if err != nil { t.Fatal(err) }
	if len(page.Items) != 2 { t.Fatalf("remaining history=%d, want 2", len(page.Items)) }
}

func TestTenantHealthHistoryRejectsInvalidCursorAndFilters(t *testing.T) {
	store, err := Open(context.Background(), ":memory:", testKey)
	if err != nil { t.Fatal(err) }
	defer store.Close()
	if _, err := store.ListTenantConnectionHealthHistory(context.Background(), TenantHealthHistoryFilter{Cursor: "!"}); err == nil {
		t.Fatal("expected invalid cursor rejection")
	}
	if _, err := store.ListTenantConnectionHealthHistory(context.Background(), TenantHealthHistoryFilter{Source: "unknown"}); err == nil {
		t.Fatal("expected invalid source rejection")
	}
}


func TestTenantHealthHistoryCursorUsesIDForEqualTimestamps(t *testing.T) {
	store, err := Open(context.Background(), ":memory:", testKey)
	if err != nil { t.Fatal(err) }
	defer store.Close()
	tenant := newTenantForHealthTest(t, store, "same-timestamp")
	stamp := time.Date(2026, 11, 8, 1, 2, 3, 0, time.UTC)
	for i := 0; i < 3; i++ {
		if err := store.RecordTenantConnectionHealthWithSource(context.Background(), TenantConnectionHealth{
			TenantID: tenant.ID, Status: "healthy", CheckedAt: stamp, Message: "connection successful",
		}, "manual"); err != nil { t.Fatal(err) }
	}
	first, err := store.ListTenantConnectionHealthHistory(context.Background(), TenantHealthHistoryFilter{Limit: 2})
	if err != nil { t.Fatal(err) }
	if len(first.Items) != 2 || first.NextCursor == "" || first.Items[0].ID <= first.Items[1].ID {
		t.Fatalf("equal timestamp order is unstable: %+v", first)
	}
	second, err := store.ListTenantConnectionHealthHistory(context.Background(), TenantHealthHistoryFilter{Limit: 2, Cursor: first.NextCursor})
	if err != nil { t.Fatal(err) }
	if len(second.Items) != 1 || second.Items[0].ID >= first.Items[1].ID {
		t.Fatalf("cursor skipped or duplicated equal-timestamp rows: first=%+v second=%+v", first, second)
	}
}


func TestTenantHealthHistoryTimeRangeIsInclusiveAndValidatesBounds(t *testing.T) {
	store, err := Open(context.Background(), ":memory:", testKey)
	if err != nil { t.Fatal(err) }
	defer store.Close()
	tenant := newTenantForHealthTest(t, store, "time-range")
	base := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		if err := store.RecordTenantConnectionHealthWithSource(context.Background(), TenantConnectionHealth{
			TenantID: tenant.ID, Status: []string{"healthy", "unhealthy", "healthy"}[i],
			CheckedAt: base.Add(time.Duration(i)*time.Minute), Message: "connection result",
		}, []string{"manual", "scheduled", "manual"}[i]); err != nil { t.Fatal(err) }
	}
	from, to := base.Add(time.Minute), base.Add(2*time.Minute)
	page, err := store.ListTenantConnectionHealthHistory(context.Background(), TenantHealthHistoryFilter{
		TenantID: tenant.ID, From: &from, To: &to, Status: "healthy", Source: "manual", Limit: 10,
	})
	if err != nil { t.Fatal(err) }
	if len(page.Items) != 1 || !page.Items[0].CheckedAt.Equal(to) {
		t.Fatalf("inclusive time range/filter mismatch: %+v", page.Items)
	}
	if _, err := store.ListTenantConnectionHealthHistory(context.Background(), TenantHealthHistoryFilter{From: &to, To: &from}); err == nil {
		t.Fatal("expected reversed time range to be rejected")
	}
	for _, raw := range []string{
		base64.RawURLEncoding.EncodeToString([]byte("not-a-time\n1")),
		base64.RawURLEncoding.EncodeToString([]byte(base.Format(time.RFC3339Nano)+"\n0")),
		base64.RawURLEncoding.EncodeToString([]byte(base.Format(time.RFC3339Nano)+"\n-1")),
	} {
		if _, err := store.ListTenantConnectionHealthHistory(context.Background(), TenantHealthHistoryFilter{Cursor: raw}); err == nil {
			t.Errorf("expected malformed cursor %q to be rejected", raw)
		}
	}
}


func TestTenantHealthSummaryUsesInclusiveRollingWindowAndEmptyRate(t *testing.T) {
	store, err := Open(context.Background(), ":memory:", testKey)
	if err != nil { t.Fatal(err) }
	defer store.Close()
	tenant := newTenantForHealthTest(t, store, "summary-window")
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	for _, stamp := range []time.Time{now.Add(-24*time.Hour), now.Add(-24*time.Hour-time.Nanosecond)} {
		if err := store.RecordTenantConnectionHealthWithSource(context.Background(), TenantConnectionHealth{
			TenantID:tenant.ID, Status:"healthy", CheckedAt:stamp, Message:"connection successful",
		}, "manual"); err != nil { t.Fatal(err) }
	}
	got, err := store.GetTenantHealthSummary(context.Background(), now)
	if err != nil { t.Fatal(err) }
	if got.TenantCount != 1 || got.CheckedTenantCount != 1 || got.HealthyTenantCount != 1 || got.UncheckedTenantCount != 0 {
		t.Fatalf("unexpected current counts: %+v", got)
	}
	if got.ChecksLast24Hours != 1 || got.HealthyChecksLast24Hours != 1 || got.UnhealthyChecksLast24Hours != 0 || got.SuccessRateLast24Hours != 100 {
		t.Fatalf("24-hour boundary was not inclusive: %+v", got)
	}

	emptyStore, err := Open(context.Background(), ":memory:", testKey)
	if err != nil { t.Fatal(err) }
	defer emptyStore.Close()
	empty, err := emptyStore.GetTenantHealthSummary(context.Background(), now)
	if err != nil { t.Fatal(err) }
	if empty.ChecksLast24Hours != 0 || empty.SuccessRateLast24Hours != 0 {
		t.Fatalf("empty summary should have a zero rate without NaN: %+v", empty)
	}
	if _, err := emptyStore.GetTenantHealthSummary(context.Background(), time.Time{}); err == nil {
		t.Fatal("zero summary time should be rejected")
	}
}


func TestTenantHealthTrendsUsesBoundedUTCZeroFilledBuckets(t *testing.T) {
	store, err := Open(context.Background(), ":memory:", testKey)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	tenant := newTenantForHealthTest(t, store, "trend")
	now := time.Date(2026, 11, 8, 12, 34, 0, 0, time.UTC)
	checks := []struct {
		at time.Time
		status string
	}{
		{at: time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC), status: "healthy"},
		{at: time.Date(2026, 11, 7, 13, 15, 0, 0, time.UTC), status: "unhealthy"},
		{at: time.Date(2026, 11, 8, 11, 59, 59, 999999999, time.UTC), status: "healthy"},
		{at: time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC), status: "unhealthy"}, // current incomplete hour
		{at: now.Add(time.Minute), status: "healthy"}, // future result
	}
	for _, check := range checks {
		if err := store.RecordTenantConnectionHealthWithSource(context.Background(), TenantConnectionHealth{
			TenantID: tenant.ID, Status: check.status, CheckedAt: check.at, Message: "trend check",
		}, "manual"); err != nil {
			t.Fatal(err)
		}
	}

	hourly, err := store.GetTenantHealthTrends(context.Background(), now, "24h")
	if err != nil {
		t.Fatal(err)
	}
	if len(hourly.Buckets) != 24 {
		t.Fatalf("hourly bucket count=%d, want 24", len(hourly.Buckets))
	}
	if !hourly.WindowStartedAt.Equal(time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)) ||
		!hourly.WindowEndedAt.Equal(time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("unexpected hourly window: %+v", hourly)
	}
	if hourly.Buckets[0].Checks != 1 || hourly.Buckets[0].Healthy != 1 ||
		hourly.Buckets[1].Checks != 1 || hourly.Buckets[1].Unhealthy != 1 ||
		hourly.Buckets[23].Checks != 1 || hourly.Buckets[23].SuccessRate != 100 {
		t.Fatalf("hourly bucket aggregation mismatch: %+v", hourly.Buckets)
	}
	for i := 2; i < 23; i++ {
		if hourly.Buckets[i].Checks != 0 || hourly.Buckets[i].SuccessRate != 0 {
			t.Fatalf("empty bucket %d was not zero-filled: %+v", i, hourly.Buckets[i])
		}
	}

	daily, err := store.GetTenantHealthTrends(context.Background(), now, "7d")
	if err != nil {
		t.Fatal(err)
	}
	if len(daily.Buckets) != 7 {
		t.Fatalf("daily bucket count=%d, want 7", len(daily.Buckets))
	}
	if !daily.WindowStartedAt.Equal(time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)) ||
		!daily.WindowEndedAt.Equal(now) {
		t.Fatalf("unexpected daily window: %+v", daily)
	}
	if daily.Buckets[5].Checks != 2 || daily.Buckets[5].Healthy != 1 || daily.Buckets[5].Unhealthy != 1 ||
		daily.Buckets[6].Checks != 2 || daily.Buckets[6].Healthy != 1 || daily.Buckets[6].Unhealthy != 1 ||
		daily.Buckets[6].SuccessRate != 50 {
		t.Fatalf("daily bucket aggregation mismatch: %+v", daily.Buckets)
	}
	if _, err := store.GetTenantHealthTrends(context.Background(), now, "30d"); err == nil {
		t.Fatal("unsupported trend range should be rejected")
	}
}

func TestTenantHealthTrendsReturnsZeroFilledSeriesWithoutHistory(t *testing.T) {
	store, err := Open(context.Background(), ":memory:", testKey)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 11, 8, 12, 34, 0, 0, time.UTC)
	got, err := store.GetTenantHealthTrends(context.Background(), now, "24h")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Buckets) != 24 {
		t.Fatalf("bucket count=%d, want 24", len(got.Buckets))
	}
	for i, bucket := range got.Buckets {
		if bucket.Checks != 0 || bucket.Healthy != 0 || bucket.Unhealthy != 0 || bucket.SuccessRate != 0 {
			t.Fatalf("empty bucket %d contains non-zero metrics: %+v", i, bucket)
		}
	}
	if _, err := store.GetTenantHealthTrends(context.Background(), time.Time{}, "24h"); err == nil {
		t.Fatal("zero trend time should be rejected")
	}
}


func TestTenantHealthListReportsConsecutiveFailuresAndLastSuccess(t *testing.T) {
	store, err := Open(context.Background(), ":memory:", testKey)
	if err != nil { t.Fatal(err) }
	defer store.Close()

	streakTenant := newTenantForHealthTest(t, store, "failure-streak")
	noSuccessTenant := newTenantForHealthTest(t, store, "no-success")
	recoveredTenant := newTenantForHealthTest(t, store, "recovered")
	base := time.Date(2026, 11, 9, 1, 0, 0, 0, time.UTC)

	record := func(tenantID, status string, at time.Time) {
		t.Helper()
		message := "connection successful"
		if status == "unhealthy" { message = "connection check failed; verify the registered data source" }
		if err := store.RecordTenantConnectionHealthWithSource(context.Background(), TenantConnectionHealth{
			TenantID: tenantID, Status: status, CheckedAt: at, Message: message,
		}, "scheduled"); err != nil {
			t.Fatal(err)
		}
	}

	// A success resets the streak. For equal timestamps, the larger history ID is newer.
	record(streakTenant.ID, "unhealthy", base)
	record(streakTenant.ID, "healthy", base.Add(time.Minute))
	record(streakTenant.ID, "unhealthy", base.Add(time.Minute))
	record(streakTenant.ID, "unhealthy", base.Add(2*time.Minute))

	record(noSuccessTenant.ID, "unhealthy", base)
	record(noSuccessTenant.ID, "unhealthy", base.Add(time.Minute))
	record(recoveredTenant.ID, "healthy", base.Add(2*time.Minute))

	items, err := store.ListTenantConnectionHealth(context.Background())
	if err != nil { t.Fatal(err) }
	byTenant := make(map[string]TenantConnectionHealth, len(items))
	for _, item := range items { byTenant[item.TenantID] = item }

	streak := byTenant[streakTenant.ID]
	if streak.ConsecutiveFailures != 2 {
		t.Fatalf("consecutive failures=%d, want 2", streak.ConsecutiveFailures)
	}
	if streak.LastSuccessAt == nil || !streak.LastSuccessAt.Equal(base.Add(time.Minute)) {
		t.Fatalf("last success=%v, want %s", streak.LastSuccessAt, base.Add(time.Minute))
	}

	noSuccess := byTenant[noSuccessTenant.ID]
	if noSuccess.ConsecutiveFailures != 2 || noSuccess.LastSuccessAt != nil {
		t.Fatalf("unexpected no-success streak: %+v", noSuccess)
	}

	recovered := byTenant[recoveredTenant.ID]
	if recovered.ConsecutiveFailures != 0 || recovered.LastSuccessAt == nil ||
		!recovered.LastSuccessAt.Equal(base.Add(2*time.Minute)) {
		t.Fatalf("unexpected recovered health: %+v", recovered)
	}
}


// TestTenantHealthScheduledResultRejectsStaleLeaseGeneration verifies that a
// replaced scheduler cannot append history or overwrite the latest health row.
func TestTenantHealthScheduledResultRejectsStaleLeaseGeneration(t *testing.T) {
	store, err := Open(context.Background(), ":memory:", testKey)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	tenant := newTenantForHealthTest(t, store, "stale-scheduler")

	now := time.Now().UTC()
	oldAcquired, oldGeneration, err := store.AcquireTenantHealthSchedulerLease(
		context.Background(), "old-scheduler", now, 5*time.Minute,
	)
	if err != nil || !oldAcquired {
		t.Fatalf("old scheduler acquire: acquired=%v generation=%d err=%v", oldAcquired, oldGeneration, err)
	}

	// Advance the lease clock beyond expiry so a replacement takes generation 2.
	newAcquired, newGeneration, err := store.AcquireTenantHealthSchedulerLease(
		context.Background(), "new-scheduler", now.Add(6*time.Minute), 5*time.Minute,
	)
	if err != nil || !newAcquired {
		t.Fatalf("new scheduler takeover: acquired=%v generation=%d err=%v", newAcquired, newGeneration, err)
	}
	if newGeneration != oldGeneration+1 {
		t.Fatalf("new generation=%d, want %d", newGeneration, oldGeneration+1)
	}

	staleResult := TenantConnectionHealth{
		TenantID: tenant.ID,
		Status: "unhealthy",
		CheckedAt: now.Add(time.Second),
		Message: "connection check failed; verify the registered data source",
	}
	err = store.RecordTenantConnectionHealthWithSchedulerLease(
		context.Background(), staleResult, "old-scheduler", oldGeneration,
	)
	if !errors.Is(err, ErrTenantHealthSchedulerLeaseLost) {
		t.Fatalf("stale generation write error=%v, want ErrTenantHealthSchedulerLeaseLost", err)
	}

	history, err := store.ListTenantConnectionHealthHistory(context.Background(), TenantHealthHistoryFilter{TenantID: tenant.ID})
	if err != nil {
		t.Fatal(err)
	}
	latest, err := store.ListTenantConnectionHealth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Items) != 0 || len(latest) != 0 {
		t.Fatalf("stale scheduler polluted health records: history=%+v latest=%+v", history.Items, latest)
	}

	currentResult := TenantConnectionHealth{
		TenantID: tenant.ID,
		Status: "healthy",
		CheckedAt: now.Add(2 * time.Second),
		Message: "connection successful",
	}
	if err := store.RecordTenantConnectionHealthWithSchedulerLease(
		context.Background(), currentResult, "new-scheduler", newGeneration,
	); err != nil {
		t.Fatalf("current generation write failed: %v", err)
	}
	history, err = store.ListTenantConnectionHealthHistory(context.Background(), TenantHealthHistoryFilter{TenantID: tenant.ID})
	if err != nil {
		t.Fatal(err)
	}
	latest, err = store.ListTenantConnectionHealth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Items) != 1 || history.Items[0].Status != "healthy" || history.Items[0].Source != "scheduled" {
		t.Fatalf("current generation history mismatch: %+v", history.Items)
	}
	if len(latest) != 1 || latest[0].Status != "healthy" {
		t.Fatalf("current generation latest snapshot mismatch: %+v", latest)
	}
}
