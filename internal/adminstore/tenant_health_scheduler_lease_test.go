package adminstore

import (
	"context"
	"fmt"
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
	now := time.Now().UTC()
	ttl := 30 * time.Second
	ok, generation, err := first.acquireTenantHealthSchedulerLeaseAt(context.Background(), "process-a", now, ttl)
	if err != nil || !ok || generation != 1 { t.Fatalf("first acquire: ok=%v generation=%d err=%v", ok, generation, err) }
	ok, _, err = second.acquireTenantHealthSchedulerLeaseAt(context.Background(), "process-b", now.Add(time.Second), ttl)
	if err != nil || ok { t.Fatalf("second process acquired active lease: ok=%v err=%v", ok, err) }
	renewed, err := first.RenewTenantHealthSchedulerLease(context.Background(), "process-a", now.Add(10*time.Second), ttl)
	if err != nil || !renewed { t.Fatalf("owner renewal: renewed=%v err=%v", renewed, err) }
	ok, _, err = second.acquireTenantHealthSchedulerLeaseAt(context.Background(), "process-b", now.Add(20*time.Second), ttl)
	if err != nil || ok { t.Fatalf("second process acquired renewed lease: ok=%v err=%v", ok, err) }
	ok, generation, err = second.acquireTenantHealthSchedulerLeaseAt(context.Background(), "process-b", now.Add(41*time.Second), ttl)
	if err != nil || !ok || generation != 2 { t.Fatalf("expired lease takeover: ok=%v generation=%d err=%v", ok, generation, err) }
	renewed, err = first.RenewTenantHealthSchedulerLease(context.Background(), "process-a", now.Add(42*time.Second), ttl)
	if err != nil || renewed { t.Fatalf("stale owner renewed replacement lease: renewed=%v err=%v", renewed, err) }
	if err := first.ReleaseTenantHealthSchedulerLeaseWithGeneration(context.Background(), "process-a", 1); err != nil { t.Fatal(err) }
	if err := second.ReleaseTenantHealthSchedulerLeaseWithGeneration(context.Background(), "process-b", 1); err != nil { t.Fatal(err) }
	var currentOwner string
	var currentGeneration int64
	if err := second.db.QueryRowContext(context.Background(), "SELECT owner_id, generation FROM admin_tenant_health_scheduler_lease WHERE id = 1").Scan(&currentOwner, &currentGeneration); err != nil { t.Fatal(err) }
	if currentOwner != "process-b" || currentGeneration != 2 {
		t.Fatalf("stale generation release changed replacement lease: owner=%q generation=%d", currentOwner, currentGeneration)
	}
	ok, _, err = first.acquireTenantHealthSchedulerLeaseAt(context.Background(), "process-a", now.Add(43*time.Second), ttl)
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
	now := time.Now().UTC()
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := 0
	errs := make(chan error, len(stores))
	for i, store := range stores {
		wg.Add(1)
		go func(i int, store *Store) {
			defer wg.Done()
			ok, _, err := store.acquireTenantHealthSchedulerLeaseAt(context.Background(), string(rune('a'+i)), now, 30*time.Second)
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
	ok, oldGeneration, err := first.acquireTenantHealthSchedulerLeaseAt(context.Background(), "old-owner", now.Add(-31*time.Second), 30*time.Second)
	if err != nil || !ok { t.Fatalf("old owner acquire: ok=%v err=%v", ok, err) }
	ok, newGeneration, err := second.acquireTenantHealthSchedulerLeaseAt(context.Background(), "new-owner", now, 30*time.Second)
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


// TestTenantHealthSchedulerLeaseExpiryBoundary verifies the lease deadline is
// exclusive for the current owner and inclusive for takeover: renewal/acquisition
// immediately before expiry remain valid, while takeover at the exact deadline
// advances the fencing generation.
func TestTenantHealthSchedulerLeaseExpiryBoundary(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "lease-boundary.db"), testKey)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	start := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	const ttl = 5 * time.Second
	acquired, generation, err := store.acquireTenantHealthSchedulerLeaseAt(context.Background(), "boundary-owner-a", start, ttl)
	if err != nil || !acquired || generation != 1 {
		t.Fatalf("initial acquisition: acquired=%v generation=%d err=%v", acquired, generation, err)
	}

	beforeExpiry := start.Add(ttl - time.Nanosecond)
	acquired, _, err = store.acquireTenantHealthSchedulerLeaseAt(context.Background(), "boundary-owner-b", beforeExpiry, ttl)
	if err != nil || acquired {
		t.Fatalf("takeover immediately before expiry: acquired=%v err=%v, want not acquired", acquired, err)
	}
	renewed, err := store.renewTenantHealthSchedulerLeaseAt(context.Background(), "boundary-owner-a", beforeExpiry, ttl)
	if err != nil || !renewed {
		t.Fatalf("renew immediately before expiry: renewed=%v err=%v, want renewed", renewed, err)
	}

	renewed, err = store.renewTenantHealthSchedulerLeaseAt(context.Background(), "boundary-owner-a", beforeExpiry.Add(ttl), ttl)
	if err != nil || renewed {
		t.Fatalf("renew exactly at renewed expiry: renewed=%v err=%v, want expired", renewed, err)
	}
	exactExpiry := beforeExpiry.Add(ttl)
	acquired, generation, err = store.acquireTenantHealthSchedulerLeaseAt(context.Background(), "boundary-owner-b", exactExpiry, ttl)
	if err != nil || !acquired || generation != 2 {
		t.Fatalf("takeover exactly at expiry: acquired=%v generation=%d err=%v, want generation 2", acquired, generation, err)
	}
}
func TestTenantHealthSchedulerLeaseRenewalUsesLiveTimeInsteadOfStaleCallerTime(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "stale-renewal.db"), testKey)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	startedAt := time.Now().UTC()
	const ttl = 5 * time.Second
	acquired, generation, err := store.AcquireTenantHealthSchedulerLease(context.Background(), "stale-renewal-owner", startedAt, ttl)
	if err != nil || !acquired || generation != 1 {
		t.Fatalf("initial acquisition: acquired=%v generation=%d err=%v", acquired, generation, err)
	}

	expiredAt := time.Now().UTC().Add(-time.Second)
	if _, err := store.db.ExecContext(context.Background(), `UPDATE admin_tenant_health_scheduler_lease
		SET lease_until = ? WHERE id = 1 AND owner_id = ? AND generation = ?`,
		expiredAt.UnixNano(), "stale-renewal-owner", generation); err != nil {
		t.Fatalf("expire lease before renewal: %v", err)
	}

	renewed, err := store.RenewTenantHealthSchedulerLease(
		context.Background(), "stale-renewal-owner", startedAt, ttl,
	)
	if err != nil {
		t.Fatal(err)
	}
	if renewed {
		t.Fatal("renewal with a stale caller timestamp revived an expired lease")
	}
	var leaseUntil int64
	if err := store.db.QueryRowContext(context.Background(), `SELECT lease_until
		FROM admin_tenant_health_scheduler_lease WHERE id = 1`).Scan(&leaseUntil); err != nil {
		t.Fatal(err)
	}
	if leaseUntil != expiredAt.UnixNano() {
		t.Fatalf("expired lease deadline changed to %d, want %d", leaseUntil, expiredAt.UnixNano())
	}
}


func TestTenantHealthSchedulerLeaseAcquisitionUsesLiveTimeInsteadOfStaleCallerTime(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "stale-acquisition.db"), testKey)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	startedAt := time.Now().UTC()
	const ttl = 5 * time.Second
	acquired, generation, err := store.AcquireTenantHealthSchedulerLease(
		context.Background(), "stale-acquisition-owner", startedAt, ttl,
	)
	if err != nil || !acquired || generation != 1 {
		t.Fatalf("initial acquisition: acquired=%v generation=%d err=%v", acquired, generation, err)
	}

	expiredAt := time.Now().UTC().Add(-time.Second)
	if _, err := store.db.ExecContext(context.Background(), `UPDATE admin_tenant_health_scheduler_lease
		SET lease_until = ? WHERE id = 1 AND owner_id = ? AND generation = ?`,
		expiredAt.UnixNano(), "stale-acquisition-owner", generation); err != nil {
		t.Fatalf("expire lease before stale acquisition: %v", err)
	}

	acquired, _, err = store.AcquireTenantHealthSchedulerLease(
		context.Background(), "new-acquisition-owner", startedAt, ttl,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !acquired {
		t.Fatal("acquisition did not use live time after the stored lease expired")
	}
	var ownerID string
	var leaseUntil int64
	if err := store.db.QueryRowContext(context.Background(), `SELECT owner_id, lease_until
		FROM admin_tenant_health_scheduler_lease WHERE id = 1`).Scan(&ownerID, &leaseUntil); err != nil {
		t.Fatal(err)
	}
	if ownerID != "new-acquisition-owner" || leaseUntil <= time.Now().UTC().UnixNano() {
		t.Fatalf("live-time acquisition did not replace expired lease: owner=%q lease_until=%d", ownerID, leaseUntil)
	}
}


func TestTenantHealthSchedulerLeaseAcquisitionSamplesClockAfterSQLiteWriteLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lease-clock-lock.db")
	first, err := Open(context.Background(), path, testKey)
	if err != nil { t.Fatal(err) }
	defer first.Close()
	second, err := Open(context.Background(), path, testKey)
	if err != nil { t.Fatal(err) }
	defer second.Close()

	start := time.Now().UTC().Truncate(time.Second)
	acquired, _, err := first.acquireTenantHealthSchedulerLeaseAt(context.Background(), "previous-owner", start, 5*time.Second)
	if err != nil || !acquired { t.Fatalf("seed lease: acquired=%v err=%v", acquired, err) }

	lockTx, err := second.db.BeginTx(context.Background(), nil)
	if err != nil { t.Fatal(err) }
	if _, err := lockTx.ExecContext(context.Background(), "UPDATE admin_tenant_health_scheduler_lease SET updated_at = updated_at WHERE id = 1"); err != nil {
		_ = lockTx.Rollback()
		t.Fatal(err)
	}
	clockCalled := make(chan struct{}, 1)
	result := make(chan error, 1)
	go func() {
		ok, generation, err := first.acquireTenantHealthSchedulerLeaseWithClock(context.Background(), "replacement-owner", func() time.Time {
			clockCalled <- struct{}{}
			return start.Add(6 * time.Second)
		}, 30*time.Second)
		if err != nil { result <- err; return }
		if !ok || generation != 2 { result <- fmt.Errorf("acquire result: ok=%v generation=%d", ok, generation); return }
		result <- nil
	}()
	select {
	case <-clockCalled:
		_ = lockTx.Rollback()
		t.Fatal("acquisition sampled clock before obtaining SQLite write lock")
	case <-time.After(150 * time.Millisecond):
	}
	if err := lockTx.Rollback(); err != nil { t.Fatal(err) }
	select {
	case err := <-result:
		if err != nil { t.Fatal(err) }
	case <-time.After(5 * time.Second):
		t.Fatal("acquisition did not finish after releasing SQLite write lock")
	}
}

func TestTenantHealthSchedulerLeaseRenewalSamplesClockAfterSQLiteWriteLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lease-renew-clock-lock.db")
	first, err := Open(context.Background(), path, testKey)
	if err != nil { t.Fatal(err) }
	defer first.Close()
	second, err := Open(context.Background(), path, testKey)
	if err != nil { t.Fatal(err) }
	defer second.Close()

	start := time.Now().UTC().Truncate(time.Second)
	acquired, _, err := first.acquireTenantHealthSchedulerLeaseAt(context.Background(), "renew-owner", start, 30*time.Second)
	if err != nil || !acquired { t.Fatalf("seed lease: acquired=%v err=%v", acquired, err) }
	lockTx, err := second.db.BeginTx(context.Background(), nil)
	if err != nil { t.Fatal(err) }
	if _, err := lockTx.ExecContext(context.Background(), "UPDATE admin_tenant_health_scheduler_lease SET updated_at = updated_at WHERE id = 1"); err != nil {
		_ = lockTx.Rollback()
		t.Fatal(err)
	}
	clockCalled := make(chan struct{}, 1)
	result := make(chan error, 1)
	go func() {
		renewed, err := first.renewTenantHealthSchedulerLeaseWithClock(context.Background(), "renew-owner", 0, func() time.Time {
			clockCalled <- struct{}{}
			return start.Add(time.Second)
		}, 30*time.Second)
		if err != nil { result <- err; return }
		if !renewed { result <- fmt.Errorf("renewal returned false after lock release"); return }
		result <- nil
	}()
	select {
	case <-clockCalled:
		_ = lockTx.Rollback()
		t.Fatal("renewal sampled clock before obtaining SQLite write lock")
	case <-time.After(150 * time.Millisecond):
	}
	if err := lockTx.Rollback(); err != nil { t.Fatal(err) }
	select {
	case err := <-result:
		if err != nil { t.Fatal(err) }
	case <-time.After(5 * time.Second):
		t.Fatal("renewal did not finish after releasing SQLite write lock")
	}
}


func TestTenantHealthSchedulerLeaseReusedOwnerIDAdvancesGeneration(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "reused-owner.db"), testKey)
	if err != nil { t.Fatal(err) }
	defer store.Close()

	start := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	const ttl = 5 * time.Second
	acquired, oldGeneration, err := store.acquireTenantHealthSchedulerLeaseAt(context.Background(), "reused-process-id", start, ttl)
	if err != nil || !acquired || oldGeneration != 1 {
		t.Fatalf("initial acquisition: acquired=%v generation=%d err=%v", acquired, oldGeneration, err)
	}

	acquired, newGeneration, err := store.acquireTenantHealthSchedulerLeaseAt(context.Background(), "reused-process-id", start.Add(ttl), ttl)
	if err != nil || !acquired || newGeneration != oldGeneration+1 {
		t.Fatalf("same owner takeover after expiry: acquired=%v generation=%d err=%v, want generation %d", acquired, newGeneration, err, oldGeneration+1)
	}

	// The previous process may still have the same owner identifier. Its old
	// fencing generation must not be able to extend the replacement lease.
	renewed, err := store.renewTenantHealthSchedulerLeaseWithGenerationAt(
		context.Background(), "reused-process-id", oldGeneration, start.Add(ttl), ttl,
	)
	if err != nil || renewed {
		t.Fatalf("stale generation renewed reused owner lease: renewed=%v err=%v", renewed, err)
	}
	renewed, err = store.renewTenantHealthSchedulerLeaseWithGenerationAt(
		context.Background(), "reused-process-id", newGeneration, start.Add(ttl), ttl,
	)
	if err != nil || !renewed {
		t.Fatalf("current generation could not renew lease: renewed=%v err=%v", renewed, err)
	}

	if err := store.ReleaseTenantHealthSchedulerLeaseWithGeneration(context.Background(), "reused-process-id", oldGeneration); err != nil {
		t.Fatal(err)
	}
	var ownerID string
	var generation int64
	var leaseUntil int64
	if err := store.db.QueryRowContext(context.Background(), "SELECT owner_id, generation, lease_until FROM admin_tenant_health_scheduler_lease WHERE id = 1").Scan(&ownerID, &generation, &leaseUntil); err != nil {
		t.Fatal(err)
	}
	if ownerID != "reused-process-id" || generation != newGeneration || leaseUntil <= start.Add(ttl).UnixNano() {
		t.Fatalf("stale generation release changed renewed lease: owner=%q generation=%d lease_until=%d", ownerID, generation, leaseUntil)
	}
}


func TestTenantHealthSchedulerLeaseGenerationExhaustionDoesNotOverflow(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "generation-exhausted.db"), testKey)
	if err != nil { t.Fatal(err) }
	defer store.Close()

	start := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	const maxGeneration int64 = 1<<63 - 1
	if _, err := store.db.ExecContext(context.Background(), `UPDATE admin_tenant_health_scheduler_lease
		SET owner_id = ?, lease_until = ?, generation = ? WHERE id = 1`,
		"expired-owner", start.Add(-time.Second).UnixNano(), maxGeneration); err != nil {
		t.Fatal(err)
	}

	acquired, generation, err := store.acquireTenantHealthSchedulerLeaseAt(
		context.Background(), "replacement-owner", start, 5*time.Second,
	)
	if acquired || generation != 0 || !errors.Is(err, ErrTenantHealthSchedulerLeaseGenerationExhausted) {
		t.Fatalf("generation exhaustion result=(%v, %d, %v), want exhausted without acquisition", acquired, generation, err)
	}

	var ownerID string
	var leaseUntil, storedGeneration int64
	if err := store.db.QueryRowContext(context.Background(), `SELECT owner_id, lease_until, generation
		FROM admin_tenant_health_scheduler_lease WHERE id = 1`).Scan(&ownerID, &leaseUntil, &storedGeneration); err != nil {
		t.Fatal(err)
	}
	if ownerID != "expired-owner" || storedGeneration != maxGeneration || leaseUntil != start.Add(-time.Second).UnixNano() {
		t.Fatalf("exhausted lease was modified: owner=%q generation=%d lease_until=%d", ownerID, storedGeneration, leaseUntil)
	}
}
