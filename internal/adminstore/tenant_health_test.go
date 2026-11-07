package adminstore

import (
	"context"
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
