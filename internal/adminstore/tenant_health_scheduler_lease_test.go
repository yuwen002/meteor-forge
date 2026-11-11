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
