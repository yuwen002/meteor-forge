package adminstore

import (
	"context"
	"errors"
	"path/filepath"
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
	if item.Status != "succeeded" || item.DeletedHistoryCount != 12 || item.DeletedRunCount != 3 || item.FinishedAt == nil || !item.FinishedAt.Equal(now.Add(time.Second)) {
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
	for _, tc := range []struct{name, owner string; generation int64}{
		{name:"wrong owner", owner:"retention-summary-owner-b", generation:generation},
		{name:"wrong generation", owner:"retention-summary-owner-a", generation:generation+1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := store.RecordTenantHealthRetentionCleanupWithLease(ctx, tc.owner, tc.generation, now, now, "succeeded", 1, 1, "")
			if !errors.Is(err, ErrTenantHealthSchedulerLeaseLost) {
				t.Fatalf("record result error=%v, want lease lost", err)
			}
		})
	}
	// Expire the persisted lease while keeping finishedAt in the past. The
	// write must compare against wall-clock time, not trust the caller timestamp.
	if _, err := store.db.ExecContext(ctx, "UPDATE admin_tenant_health_scheduler_lease SET lease_until = ? WHERE id = 1", time.Now().UTC().Add(-time.Second).UnixNano()); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordTenantHealthRetentionCleanupWithLease(ctx, "retention-summary-owner-a", generation, now, now, "succeeded", 1, 1, ""); !errors.Is(err, ErrTenantHealthSchedulerLeaseLost) {
		t.Fatalf("expired lease record error=%v, want lease lost", err)
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
	if _, err := store.db.ExecContext(ctx, "UPDATE admin_tenant_health_scheduler_lease SET lease_until = ? WHERE id = 1", time.Now().UTC().Add(-time.Second).UnixNano()); err != nil {
		t.Fatal(err)
	}
	if deleted, err := store.DeleteExpiredTenantConnectionHealthHistoryWithLease(ctx, "history-retention-owner", generation, now, before, 500); deleted != 0 || !errors.Is(err, ErrTenantHealthSchedulerLeaseLost) {
		t.Fatalf("expired lease deletion=(%d,%v), want lease lost", deleted, err)
	}
}


func TestTenantHealthRetentionStateSamplesLeaseTimeAfterWriteLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "retention-state-lock-expiry.db")
	first, err := Open(context.Background(), path, testKey)
	if err != nil { t.Fatal(err) }
	defer first.Close()
	first.db.SetMaxOpenConns(1)
	if _, err := first.db.ExecContext(context.Background(), "PRAGMA busy_timeout = 10000"); err != nil { t.Fatal(err) }
	second, err := Open(context.Background(), path, testKey)
	if err != nil { t.Fatal(err) }
	defer second.Close()

	now := time.Now().UTC()
	acquired, generation, err := first.AcquireTenantHealthSchedulerLease(context.Background(), "retention-state-lock-owner", now, 5*time.Second)
	if err != nil || !acquired { t.Fatalf("acquire lease: acquired=%v err=%v", acquired, err) }

	lockTx, err := second.db.BeginTx(context.Background(), nil)
	if err != nil { t.Fatal(err) }
	if _, err := lockTx.ExecContext(context.Background(), "UPDATE admin_tenant_health_scheduler_lease SET updated_at = updated_at WHERE id = 1"); err != nil {
		_ = lockTx.Rollback()
		t.Fatal(err)
	}

	recordResult := make(chan error, 1)
	go func() {
		recordResult <- first.RecordTenantHealthRetentionCleanupWithLease(
			context.Background(), "retention-state-lock-owner", generation,
			now, now.Add(time.Second), "succeeded", 12, 3, "",
		)
	}()

	// Hold the independent writer lock beyond the minimum lease duration.
	time.Sleep(5500 * time.Millisecond)
	if err := lockTx.Rollback(); err != nil { t.Fatal(err) }
	select {
	case err := <-recordResult:
		if !errors.Is(err, ErrTenantHealthSchedulerLeaseLost) {
			t.Fatalf("record after lock wait error=%v, want lease lost", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("retention state write did not return after releasing SQLite lock")
	}
	item, err := first.GetTenantHealthRetentionCleanup(context.Background())
	if err != nil { t.Fatal(err) }
	if item.Status != "never_run" || item.DeletedHistoryCount != 0 || item.DeletedRunCount != 0 {
		t.Fatalf("expired lease overwrote cleanup state after lock wait: %+v", item)
	}
}

func TestTenantConnectionHealthHistoryRetentionSamplesLeaseTimeAfterWriteLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tenant-history-lock-expiry.db")
	first, err := Open(context.Background(), path, testKey)
	if err != nil { t.Fatal(err) }
	defer first.Close()
	first.db.SetMaxOpenConns(1)
	if _, err := first.db.ExecContext(context.Background(), "PRAGMA busy_timeout = 10000"); err != nil { t.Fatal(err) }
	second, err := Open(context.Background(), path, testKey)
	if err != nil { t.Fatal(err) }
	defer second.Close()

	ctx := context.Background()
	source, err := first.Create(ctx, DataSourceInput{Name: "history-lock-retention", Driver: "sqlite", DSN: ":memory:"})
	if err != nil { t.Fatal(err) }
	tenant, err := first.CreateTenant(ctx, TenantInput{Name: "History lock tenant", Code: "history-lock-retention", DataSourceID: source.ID})
	if err != nil { t.Fatal(err) }
	now := time.Now().UTC()
	if err := first.RecordTenantConnectionHealth(ctx, TenantConnectionHealth{
		TenantID: tenant.ID, Status: "healthy", CheckedAt: now.Add(-40*24*time.Hour), Message: "old result",
	}); err != nil { t.Fatal(err) }
	acquired, generation, err := first.AcquireTenantHealthSchedulerLease(ctx, "history-lock-retention-owner", now, 5*time.Second)
	if err != nil || !acquired { t.Fatalf("acquire lease: acquired=%v err=%v", acquired, err) }

	lockTx, err := second.db.BeginTx(ctx, nil)
	if err != nil { t.Fatal(err) }
	if _, err := lockTx.ExecContext(ctx, "UPDATE admin_tenant_health_scheduler_lease SET updated_at = updated_at WHERE id = 1"); err != nil {
		_ = lockTx.Rollback()
		t.Fatal(err)
	}

	deleteResult := make(chan error, 1)
	go func() {
		_, err := first.DeleteExpiredTenantConnectionHealthHistoryWithLease(
			ctx, "history-lock-retention-owner", generation, now, now.Add(-30*24*time.Hour), 500,
		)
		deleteResult <- err
	}()

	// Hold the independent writer lock beyond the minimum lease duration.
	time.Sleep(5500 * time.Millisecond)
	if err := lockTx.Rollback(); err != nil { t.Fatal(err) }
	select {
	case err := <-deleteResult:
		if !errors.Is(err, ErrTenantHealthSchedulerLeaseLost) {
			t.Fatalf("history delete after lock wait error=%v, want lease lost", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("history retention delete did not return after releasing SQLite lock")
	}
	var count int
	if err := first.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM admin_tenant_connection_health_history WHERE tenant_id = ?", tenant.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expired lease deleted history after lock wait: remaining rows=%d, want 1", count)
	}
}
