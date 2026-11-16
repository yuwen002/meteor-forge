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
	if err := store.RecordTenantHealthRetentionCleanupWithLease(ctx, "retention-summary-owner", generation, now.Add(time.Second), "succeeded", 12, 3, ""); err != nil {
		t.Fatal(err)
	}
	item, err = store.GetTenantHealthRetentionCleanup(ctx)
	if err != nil { t.Fatal(err) }
	if item.Status != "succeeded" || item.DeletedHistoryCount != 12 || item.DeletedRunCount != 3 || item.FinishedAt == nil {
		t.Fatalf("unexpected successful retention state: %+v", item)
	}
	if err := store.RecordTenantHealthRetentionCleanupWithLease(ctx, "retention-summary-owner", generation, now.Add(2*time.Second), "failed", 4, 1, "unexpected-details"); err != nil {
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
			err := store.RecordTenantHealthRetentionCleanupWithLease(ctx, tc.owner, tc.generation, tc.at, "succeeded", 1, 1, "")
			if !errors.Is(err, ErrTenantHealthSchedulerLeaseLost) {
				t.Fatalf("record result error=%v, want lease lost", err)
			}
		})
	}
	item, err := store.GetTenantHealthRetentionCleanup(ctx)
	if err != nil { t.Fatal(err) }
	if item.Status != "never_run" { t.Fatalf("stale instance changed retention state: %+v", item) }
}
