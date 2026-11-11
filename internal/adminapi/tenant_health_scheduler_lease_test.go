package adminapi

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
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
