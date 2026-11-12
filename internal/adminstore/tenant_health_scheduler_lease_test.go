package adminstore

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestTenantHealthSchedulerLeaseCoordinatesIndependentStoreHandles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.db")
	first, err := Open(context.Background(), path, testKey)
	if err != nil { t.Fatal(err) }
	defer first.Close()
	second, err := Open(context.Background(), path, testKey)
	if err != nil { t.Fatal(err) }
	defer second.Close()
	now := time.Date(2026, 11, 11, 6, 0, 0, 0, time.UTC)
	ttl := 30 * time.Second
	ok, generation, err := first.AcquireTenantHealthSchedulerLease(context.Background(), "process-a", now, ttl)
	if err != nil || !ok || generation != 1 { t.Fatalf("first acquire: ok=%v generation=%d err=%v", ok, generation, err) }
	ok, _, err = second.AcquireTenantHealthSchedulerLease(context.Background(), "process-b", now.Add(time.Second), ttl)
	if err != nil || ok { t.Fatalf("second process acquired active lease: ok=%v err=%v", ok, err) }
	renewed, err := first.RenewTenantHealthSchedulerLease(context.Background(), "process-a", now.Add(10*time.Second), ttl)
	if err != nil || !renewed { t.Fatalf("owner renewal: renewed=%v err=%v", renewed, err) }
	ok, _, err = second.AcquireTenantHealthSchedulerLease(context.Background(), "process-b", now.Add(20*time.Second), ttl)
	if err != nil || ok { t.Fatalf("second process acquired renewed lease: ok=%v err=%v", ok, err) }
	ok, generation, err = second.AcquireTenantHealthSchedulerLease(context.Background(), "process-b", now.Add(41*time.Second), ttl)
	if err != nil || !ok || generation != 2 { t.Fatalf("expired lease takeover: ok=%v generation=%d err=%v", ok, generation, err) }
	renewed, err = first.RenewTenantHealthSchedulerLease(context.Background(), "process-a", now.Add(42*time.Second), ttl)
	if err != nil || renewed { t.Fatalf("stale owner renewed replacement lease: renewed=%v err=%v", renewed, err) }
	if err := first.ReleaseTenantHealthSchedulerLease(context.Background(), "process-a"); err != nil { t.Fatal(err) }
	ok, _, err = first.AcquireTenantHealthSchedulerLease(context.Background(), "process-a", now.Add(43*time.Second), ttl)
	if err != nil || ok { t.Fatalf("stale owner release cleared replacement lease: ok=%v err=%v", ok, err) }
}

func TestTenantHealthSchedulerLeaseConcurrentAcquisitionHasSingleWinner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.db")
	stores := make([]*Store, 4)
	for i := range stores {
		store, err := Open(context.Background(), path, testKey)
		if err != nil { t.Fatal(err) }
		stores[i] = store
		defer store.Close()
	}
	now := time.Date(2026, 11, 11, 6, 0, 0, 0, time.UTC)
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := 0
	errs := make(chan error, len(stores))
	for i, store := range stores {
		wg.Add(1)
		go func(i int, store *Store) {
			defer wg.Done()
			ok, _, err := store.AcquireTenantHealthSchedulerLease(context.Background(), string(rune('a'+i)), now, 30*time.Second)
			if err != nil { errs <- err; return }
			if ok { mu.Lock(); winners++; mu.Unlock() }
		}(i, store)
	}
	wg.Wait()
	close(errs)
	for err := range errs { t.Fatal(err) }
	if winners != 1 { t.Fatalf("lease acquisition winners=%d, want exactly one", winners) }
}

func TestTenantHealthSchedulerLeaseRejectsInvalidArguments(t *testing.T) {
	store, err := Open(context.Background(), ":memory:", testKey)
	if err != nil { t.Fatal(err) }
	defer store.Close()
	if _, _, err := store.AcquireTenantHealthSchedulerLease(context.Background(), "", time.Now(), 30*time.Second); err == nil {
		t.Fatal("expected empty owner to be rejected")
	}
	if _, err := store.RenewTenantHealthSchedulerLease(context.Background(), "owner", time.Now(), time.Second); err == nil {
		t.Fatal("expected too-short lease to be rejected")
	}
}

func TestTenantHealthSchedulerLeaseFencesStaleResultWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fenced-health.db")
	first, err := Open(context.Background(), path, testKey)
	if err != nil { t.Fatal(err) }
	defer first.Close()
	second, err := Open(context.Background(), path, testKey)
	if err != nil { t.Fatal(err) }
	defer second.Close()
	tenant := newTenantForHealthTest(t, first, "lease-fence")
	now := time.Now().UTC()
	ok, oldGeneration, err := first.AcquireTenantHealthSchedulerLease(context.Background(), "old-owner", now.Add(-31*time.Second), 30*time.Second)
	if err != nil || !ok { t.Fatalf("old owner acquire: ok=%v err=%v", ok, err) }
	ok, newGeneration, err := second.AcquireTenantHealthSchedulerLease(context.Background(), "new-owner", now, 30*time.Second)
	if err != nil || !ok || newGeneration <= oldGeneration { t.Fatalf("new owner takeover: ok=%v generation=%d err=%v", ok, newGeneration, err) }
	item := TenantConnectionHealth{TenantID: tenant.ID, Status: "healthy", CheckedAt: now, Message: "connection successful"}
	err = first.RecordTenantConnectionHealthWithSchedulerLease(context.Background(), item, "old-owner", oldGeneration)
	if err != ErrTenantHealthSchedulerLeaseLost { t.Fatalf("stale result write error=%v, want lease lost", err) }
	page, err := second.ListTenantConnectionHealthHistory(context.Background(), TenantHealthHistoryFilter{TenantID: tenant.ID})
	if err != nil { t.Fatal(err) }
	if len(page.Items) != 0 { t.Fatalf("stale owner wrote %d history rows", len(page.Items)) }
	if err := second.RecordTenantConnectionHealthWithSchedulerLease(context.Background(), item, "new-owner", newGeneration); err != nil {
		t.Fatalf("current owner result write failed: %v", err)
	}
	page, err = second.ListTenantConnectionHealthHistory(context.Background(), TenantHealthHistoryFilter{TenantID: tenant.ID})
	if err != nil { t.Fatal(err) }
	if len(page.Items) != 1 { t.Fatalf("current owner history rows=%d, want 1", len(page.Items)) }
}

func TestTenantHealthSchedulerLeaseInfoRedactsOwnerAndReportsExpiry(t *testing.T) {
	store, err := Open(context.Background(), ":memory:", testKey)
	if err != nil { t.Fatal(err) }
	defer store.Close()
	now := time.Now().UTC()
	info, err := store.GetTenantHealthSchedulerLeaseInfo(context.Background(), "instance-a", now)
	if err != nil { t.Fatal(err) }
	if info.Status != "expired_or_unclaimed" || info.LeaseUntil != nil || info.Generation != 0 {
		t.Fatalf("unexpected unclaimed lease info: %+v", info)
	}
	acquired, generation, err := store.AcquireTenantHealthSchedulerLease(context.Background(), "secret-owner-id", now, 30*time.Second)
	if err != nil || !acquired { t.Fatalf("acquire lease: acquired=%v err=%v", acquired, err) }
	info, err = store.GetTenantHealthSchedulerLeaseInfo(context.Background(), "secret-owner-id", now.Add(time.Second))
	if err != nil { t.Fatal(err) }
	if info.Status != "held_by_this_instance" || info.LeaseUntil == nil || info.Generation != generation {
		t.Fatalf("unexpected owned lease info: %+v", info)
	}
	info, err = store.GetTenantHealthSchedulerLeaseInfo(context.Background(), "instance-b", now.Add(time.Second))
	if err != nil { t.Fatal(err) }
	if info.Status != "held_by_other_instance" || info.LeaseUntil == nil {
		t.Fatalf("unexpected remote lease info: %+v", info)
	}
	info, err = store.GetTenantHealthSchedulerLeaseInfo(context.Background(), "instance-b", now.Add(31*time.Second))
	if err != nil { t.Fatal(err) }
	if info.Status != "expired_or_unclaimed" || info.LeaseUntil != nil || info.Generation != generation {
		t.Fatalf("unexpected expired lease info: %+v", info)
	}
}


// TestTenantHealthSchedulerLeaseAcquisitionRecoversAfterTransientSQLiteLock
// holds a real SQLite write lock, verifies lease acquisition fails, then releases
// the lock and verifies the same Store can acquire the lease without reopening.
func TestTenantHealthSchedulerLeaseAcquisitionRecoversAfterTransientSQLiteLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transient-lock.db")
	first, err := Open(context.Background(), path, testKey)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	second, err := Open(context.Background(), path, testKey)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	lockTx, err := second.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockTx.ExecContext(context.Background(), `UPDATE admin_tenant_health_scheduler_lease
		SET updated_at = updated_at WHERE id = 1`); err != nil {
		_ = lockTx.Rollback()
		t.Fatalf("hold SQLite metadata write lock: %v", err)
	}

	acquireCtx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if acquired, _, err := first.AcquireTenantHealthSchedulerLease(acquireCtx, "recovered-owner", time.Now().UTC(), 30*time.Second); err == nil || acquired {
		t.Fatalf("lease acquisition during SQLite lock: acquired=%v err=%v, want transient database error", acquired, err)
	}

	if err := lockTx.Rollback(); err != nil {
		t.Fatalf("release SQLite metadata write lock: %v", err)
	}

	now := time.Now().UTC()
	acquired, generation, err := first.AcquireTenantHealthSchedulerLease(context.Background(), "recovered-owner", now, 30*time.Second)
	if err != nil || !acquired || generation != 1 {
		t.Fatalf("lease acquisition after SQLite recovery: acquired=%v generation=%d err=%v", acquired, generation, err)
	}
	info, err := first.GetTenantHealthSchedulerLeaseInfo(context.Background(), "recovered-owner", now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if info.Status != "held_by_this_instance" || info.Generation != generation {
		t.Fatalf("unexpected lease after recovery: %+v", info)
	}
}
