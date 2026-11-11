package adminapi

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yuwen002/meteor-forge/internal/adminstore"
)

func TestRunWithTenantHealthSchedulerLeaseSerializesInstances(t *testing.T) {
	store, err := adminstore.Open(context.Background(), filepath.Join(t.TempDir(), "scheduler.db"), strings.Repeat("0", 64))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- runWithTenantHealthSchedulerLease(ctx, store, "api-instance-a", func(runCtx context.Context) error {
			close(started)
			<-runCtx.Done()
			return runCtx.Err()
		})
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("first instance did not acquire the scheduler lease")
	}

	secondRan := false
	err = runWithTenantHealthSchedulerLease(context.Background(), store, "api-instance-b", func(context.Context) error {
		secondRan = true
		return nil
	})
	if !errors.Is(err, errTenantHealthSchedulerLeaseNotAcquired) || secondRan {
		t.Fatalf("second instance result: err=%v ran=%v, want lease-not-acquired and no execution", err, secondRan)
	}

	cancel()
	select {
	case err := <-firstDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("first instance shutdown error=%v, want context canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first instance did not release its lease after cancellation")
	}

	err = runWithTenantHealthSchedulerLease(context.Background(), store, "api-instance-b", func(context.Context) error {
		secondRan = true
		return nil
	})
	if err != nil || !secondRan {
		t.Fatalf("second instance could not acquire released lease: err=%v ran=%v", err, secondRan)
	}
}

// TestRunWithTenantHealthSchedulerLeaseStopsWhenMetadataDBBecomesUnavailable
// simulates an abrupt metadata-store outage after acquisition. The round must
// receive cancellation and the wrapper must report lease loss.
func TestRunWithTenantHealthSchedulerLeaseStopsWhenMetadataDBBecomesUnavailable(t *testing.T) {
	store, err := adminstore.Open(context.Background(), filepath.Join(t.TempDir(), "scheduler-outage.db"), strings.Repeat("0", 64))
	if err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	canceled := make(chan struct{})
	runDone := make(chan error, 1)
	go func() {
		runDone <- runWithTenantHealthSchedulerLeaseTTL(context.Background(), store, "api-instance-outage", 5*time.Second, func(runCtx context.Context) error {
			close(started)
			<-runCtx.Done()
			close(canceled)
			return runCtx.Err()
		})
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		_ = store.Close()
		t.Fatal("scheduler round did not start")
	}

	// Closing the pool models the metadata database becoming unavailable. The
	// next heartbeat (at ttl/3) must cancel the round within a bounded interval.
	if err := store.Close(); err != nil {
		t.Fatalf("close metadata store to simulate outage: %v", err)
	}
	select {
	case <-canceled:
	case <-time.After(4 * time.Second):
		t.Fatal("scheduler round continued after metadata database outage")
	}
	select {
	case err := <-runDone:
		if !errors.Is(err, errTenantHealthSchedulerLeaseLost) {
			t.Fatalf("lease wrapper error=%v, want lease-lost sentinel", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("lease wrapper did not return after metadata database outage")
	}
}


// TestRunWithTenantHealthSchedulerLeaseConcurrentInstancesHasSingleRunner
// verifies the API-level wrapper, not only the underlying conditional UPDATE:
// exactly one contender may enter the round callback.
func TestRunWithTenantHealthSchedulerLeaseConcurrentInstancesHasSingleRunner(t *testing.T) {
	const contenders = 4
	path := filepath.Join(t.TempDir(), "concurrent-schedulers.db")
	stores := make([]*adminstore.Store, contenders)
	for i := range stores {
		store, err := adminstore.Open(context.Background(), path, strings.Repeat("0", 64))
		if err != nil { t.Fatal(err) }
		stores[i] = store
		defer store.Close()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := make(chan struct{})
	entered := make(chan int, contenders)
	results := make(chan error, contenders)
	var runs atomic.Int32
	var wg sync.WaitGroup
	for i, store := range stores {
		wg.Add(1)
		go func(i int, store *adminstore.Store) {
			defer wg.Done()
			<-start
			results <- runWithTenantHealthSchedulerLease(ctx, store, fmt.Sprintf("parallel-instance-%d", i), func(runCtx context.Context) error {
				runs.Add(1)
				entered <- i
				<-runCtx.Done()
				return runCtx.Err()
			})
		}(i, store)
	}
	close(start)

	var winner int
	select {
	case winner = <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("no contender entered the scheduled round")
	}
	// Every losing contender returns without entering its callback. Wait for
	// those results before canceling the winner so the race is fully observed.
	losers := 0
	for losers < contenders-1 {
		select {
		case err := <-results:
			if !errors.Is(err, errTenantHealthSchedulerLeaseNotAcquired) {
				t.Fatalf("non-winning contender returned %v", err)
			}
			losers++
		case <-time.After(3 * time.Second):
			t.Fatalf("only %d/%d losing contenders returned", losers, contenders-1)
		}
	}
	if got := runs.Load(); got != 1 {
		t.Fatalf("scheduled round callbacks=%d, want exactly one (winner %d)", got, winner)
	}
	cancel()
	wg.Wait()
	for i := 0; i < 1; i++ {
		select {
		case err := <-results:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("winning contender result=%v, want context canceled", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("winning scheduler did not stop after cancellation")
		}
	}
}

// TestRunWithTenantHealthSchedulerLeaseFencesOldOwnerAfterTakeover forces the
// lease deadline forward instead of sleeping for the full TTL. It verifies
// that a resumed old owner is canceled and cannot release the replacement lease.
func TestRunWithTenantHealthSchedulerLeaseFencesOldOwnerAfterTakeover(t *testing.T) {
	path := filepath.Join(t.TempDir(), "takeover.db")
	oldStore, err := adminstore.Open(context.Background(), path, strings.Repeat("0", 64))
	if err != nil { t.Fatal(err) }
	defer oldStore.Close()
	newStore, err := adminstore.Open(context.Background(), path, strings.Repeat("0", 64))
	if err != nil { t.Fatal(err) }
	defer newStore.Close()

	started := make(chan struct{})
	stopped := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- runWithTenantHealthSchedulerLeaseTTL(context.Background(), oldStore, "old-process", 5*time.Second, func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			close(stopped)
			return ctx.Err()
		})
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("old process did not acquire lease")
	}

	// Advance only the store operation's logical time. The old process's real
	// heartbeat will observe that its owner ID has been replaced.
	future := time.Now().UTC().Add(6 * time.Second)
	acquired, newGeneration, err := newStore.AcquireTenantHealthSchedulerLease(context.Background(), "new-process", future, 5*time.Second)
	if err != nil || !acquired || newGeneration != 2 {
		t.Fatalf("replacement process takeover: acquired=%v generation=%d err=%v", acquired, newGeneration, err)
	}
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("old process continued after losing its lease")
	}
	select {
	case err := <-done:
		if !errors.Is(err, errTenantHealthSchedulerLeaseLost) {
			t.Fatalf("old process returned %v, want lease lost", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("old process did not finish after lease loss")
	}

	info, err := newStore.GetTenantHealthSchedulerLeaseInfo(context.Background(), "new-process", future.Add(time.Second))
	if err != nil { t.Fatal(err) }
	if info.Status != "held_by_this_instance" || info.Generation != newGeneration {
		t.Fatalf("old owner's deferred release damaged replacement lease: %+v", info)
	}
}
