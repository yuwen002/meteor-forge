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
	if err != nil { t.Fatal(err) }
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
		if !errors.Is(err, context.Canceled) { t.Fatalf("first instance shutdown error=%v, want context canceled", err) }
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
