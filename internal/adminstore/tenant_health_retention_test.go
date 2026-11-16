package adminstore

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestTenantHealthRetentionCleanupPersistsLatestResult(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	item, err := store.GetTenantHealthRetentionCleanup(ctx)
	if err != nil { t.Fatal(err) }
	if item.Status != "never_run" || item.DeletedHistoryCount != 0 || item.DeletedRunCount != 0 {
		t.Fatalf("unexpected initial retention state: %+v", item)
	}
	acquired, generation, err := store.AcquireTenantHealthSchedulerLease(ctx, "retention-summary-owner", now, time.Minute)
	if err != nil || !acquired { t.Fatalf("acquire lease: acquired=%v err=%v", acquired, err) }
	if err := store.RecordTenantHealthRetentionCleanupWithLease(ctx, "retention-summary-owner", generation, now, now.Add(time.Second), "succeeded", 12, 3, ""); err != nil {
		t.Fatal(err)
	}
	item, err = store.GetTenantHealthRetentionCleanup(ctx)
	if err != nil { t.Fatal(err) }
	if item.Status != "succeeded" || item.DeletedHistoryCount != 12 || item.DeletedRunCount != 3 || item.FinishedAt == nil {
		t.Fatalf("unexpected successful retention state: %+v", item)
	}
	if err := store.RecordTenantHealthRetentionCleanupWithLease(ctx, "retention-summary-owner", generation, now.Add(time.Second), now.Add(2*time.Second), "failed", 4, 1, "unexpected-details"); err != nil {
		t.Fatal(err)
	}
	item, err = store.GetTenantHealthRetentionCleanup(ctx)
	if err != nil { t.Fatal(err) }
	if item.Status != "failed" || item.ErrorCode != "cleanup_failed" || item.DeletedHistoryCount != 4 || item.DeletedRunCount != 1 {
		t.Fatalf("unexpected failed retention state: %+v", item)
	}
}

func TestTenantHealthRetentionCleanupRequiresCurrentLease(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	acquired, generation, err := store.AcquireTenantHealthSchedulerLease(ctx, "retention-summary-owner-a", now, 5*time.Second)
	if err != nil || !acquired { t.Fatalf("acquire lease: acquired=%v err=%v", acquired, err) }
	for _, tc := range []struct{name, owner string; generation int64; at time.Time}{
		{name:"wrong owner", owner:"retention-summary-owner-b", generation:generation, at:now},
		{name:"wrong generation", owner:"retention-summary-owner-a", generation:generation+1, at:now},
		{name:"expired lease", owner:"retention-summary-owner-a", generation:generation, at:now.Add(6*time.Second)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := store.RecordTenantHealthRetentionCleanupWithLease(ctx, tc.owner, tc.generation, now, tc.at, "succeeded", 1, 1, "")
			if !errors.Is(err, ErrTenantHealthSchedulerLeaseLost) {
				t.Fatalf("record result error=%v, want lease lost", err)
			}
		})
	}
	item, err := store.GetTenantHealthRetentionCleanup(ctx)
	if err != nil { t.Fatal(err) }
	if item.Status != "never_run" { t.Fatalf("stale instance changed retention state: %+v", item) }
}


func TestTenantConnectionHealthHistoryRetentionRequiresCurrentLease(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	source, err := store.Create(ctx, DataSourceInput{Name: "retention-history", Driver: "sqlite", DSN: ":memory:"})
	if err != nil { t.Fatal(err) }
	tenant, err := store.CreateTenant(ctx, TenantInput{Name: "Retention tenant", Code: "retention-history", DataSourceID: source.ID})
	if err != nil { t.Fatal(err) }
	now := time.Now().UTC().Truncate(time.Second)
	if err := store.RecordTenantConnectionHealth(ctx, TenantConnectionHealth{
		TenantID: tenant.ID, Status: "healthy", CheckedAt: now.Add(-40*24*time.Hour), Message: "old result",
	}); err != nil { t.Fatal(err) }
	acquired, generation, err := store.AcquireTenantHealthSchedulerLease(ctx, "history-retention-owner", now, time.Minute)
	if err != nil || !acquired { t.Fatalf("acquire lease: acquired=%v err=%v", acquired, err) }
	before := now.Add(-30*24*time.Hour)
	if deleted, err := store.DeleteExpiredTenantConnectionHealthHistoryWithLease(ctx, "wrong-owner", generation, now, before, 500); deleted != 0 || !errors.Is(err, ErrTenantHealthSchedulerLeaseLost) {
		t.Fatalf("wrong owner deletion=(%d,%v), want lease lost", deleted, err)
	}
	if deleted, err := store.DeleteExpiredTenantConnectionHealthHistoryWithLease(ctx, "history-retention-owner", generation, now, before, 500); err != nil || deleted != 1 {
		t.Fatalf("current owner deletion=(%d,%v), want one row", deleted, err)
	}
}
