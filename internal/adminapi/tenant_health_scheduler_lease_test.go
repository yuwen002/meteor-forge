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

	// Release the old lease to simulate a process handoff. The old process's
	// heartbeat will observe that ownership has been cleared before takeover.
	if err := oldStore.ReleaseTenantHealthSchedulerLeaseWithGeneration(context.Background(), "old-process", 1); err != nil {
		t.Fatalf("release old lease for takeover: %v", err)
	}
	takeoverAt := time.Now().UTC()
	acquired, newGeneration, err := newStore.AcquireTenantHealthSchedulerLease(context.Background(), "new-process", takeoverAt, 5*time.Second)
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

	info, err := newStore.GetTenantHealthSchedulerLeaseInfo(context.Background(), "new-process", takeoverAt.Add(time.Second))
	if err != nil { t.Fatal(err) }
	if info.Status != "held_by_this_instance" || info.Generation != newGeneration {
		t.Fatalf("old owner's deferred release damaged replacement lease: %+v", info)
	}
}

// TestTenantHealthSchedulerLeaseRepeatedTakeoversKeepGenerationMonotonic
// simulates repeated process crashes and recoveries without waiting for real TTLs.
// Every replacement must advance the fencing generation, and stale owners must
// neither renew nor release the current owner's lease.
func TestTenantHealthSchedulerLeaseRepeatedTakeoversKeepGenerationMonotonic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repeated-takeovers.db")
	stores := make([]*adminstore.Store, 3)
	for i := range stores {
		store, err := adminstore.Open(context.Background(), path, strings.Repeat("0", 64))
		if err != nil {
			t.Fatal(err)
		}
		stores[i] = store
		defer store.Close()
	}

	const ttl = 5 * time.Second
	const takeovers = 12
	previousOwner := ""
	var currentOwner string
	var currentGeneration int64
	var currentNow time.Time

	for i := 0; i < takeovers; i++ {
		currentOwner = fmt.Sprintf("recovered-instance-%02d", i)
		currentNow = time.Now().UTC()
		store := stores[i%len(stores)]
		acquired, generation, err := store.AcquireTenantHealthSchedulerLease(
			context.Background(), currentOwner, currentNow, ttl,
		)
		if err != nil || !acquired {
			t.Fatalf("takeover %d: acquired=%v generation=%d err=%v", i+1, acquired, generation, err)
		}
		wantGeneration := int64(i + 1)
		if generation != wantGeneration {
			t.Fatalf("takeover %d generation=%d, want %d", i+1, generation, wantGeneration)
		}
		currentGeneration = generation

		if previousOwner != "" {
			staleStore := stores[(i-1+len(stores))%len(stores)]
			renewed, err := staleStore.RenewTenantHealthSchedulerLease(
				context.Background(), previousOwner, currentNow, ttl,
			)
			if err != nil {
				t.Fatalf("stale owner renewal after takeover %d: %v", i+1, err)
			}
			if renewed {
				t.Fatalf("stale owner %q renewed lease after takeover %d", previousOwner, i+1)
			}
			if err := staleStore.ReleaseTenantHealthSchedulerLease(context.Background(), previousOwner); err != nil {
				t.Fatalf("stale owner release after takeover %d: %v", i+1, err)
			}
		}

		info, err := store.GetTenantHealthSchedulerLeaseInfo(context.Background(), currentOwner, currentNow.Add(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if info.Status != "held_by_this_instance" || info.Generation != currentGeneration {
			t.Fatalf("takeover %d current lease info=%+v", i+1, info)
		}
		// Keep the final simulated owner active so the post-loop assertion
		// verifies the current lease rather than an intentionally released one.
		if i < takeovers-1 {
			if err := store.ReleaseTenantHealthSchedulerLease(context.Background(), currentOwner); err != nil {
				t.Fatalf("release simulated process %q: %v", currentOwner, err)
			}
		}
		previousOwner = currentOwner
	}

	info, err := stores[(takeovers-1)%len(stores)].GetTenantHealthSchedulerLeaseInfo(
		context.Background(), currentOwner, currentNow.Add(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if info.Status != "held_by_this_instance" || info.Generation != takeovers {
		t.Fatalf("final lease info=%+v, want current owner and generation %d", info, takeovers)
	}
}

func TestTenantHealthSchedulerLeaseHeartbeatErrorPrefersShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := tenantHealthSchedulerLeaseHeartbeatError(ctx, false, errors.New("database is closed")); err != nil {
		t.Fatalf("heartbeat error during shutdown=%v, want nil", err)
	}
}

func TestTenantHealthSchedulerLeaseHeartbeatErrorClassifiesLostLease(t *testing.T) {
	ctx := context.Background()
	if err := tenantHealthSchedulerLeaseHeartbeatError(ctx, false, nil); !errors.Is(err, errTenantHealthSchedulerLeaseLost) {
		t.Fatalf("heartbeat error=%v, want lease lost", err)
	}
	cause := errors.New("metadata database unavailable")
	if err := tenantHealthSchedulerLeaseHeartbeatError(ctx, false, cause); !errors.Is(err, errTenantHealthSchedulerLeaseLost) || !errors.Is(err, cause) {
		t.Fatalf("heartbeat error=%v, want lease lost and original cause", err)
	}
}

func TestRunWithTenantHealthSchedulerLeaseClassifiesEarlyFencedWrite(t *testing.T) {
	store, err := adminstore.Open(context.Background(), filepath.Join(t.TempDir(), "early-fenced-write.db"), strings.Repeat("0", 64))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	err = runWithTenantHealthSchedulerLeaseTTL(
		context.Background(),
		store,
		"early-fenced-writer",
		5*time.Second,
		func(context.Context) error {
			return fmt.Errorf("persist scheduled health result: %w", adminstore.ErrTenantHealthSchedulerLeaseLost)
		},
	)
	if !errors.Is(err, errTenantHealthSchedulerLeaseLost) {
		t.Fatalf("lease wrapper error=%v, want scheduler lease-lost classification", err)
	}
}


// TestTenantHealthSchedulerTakeoverFencesLateWorkerWrites exercises two
// independent API instances sharing one metadata database. A late worker from
// the old round must not persist health or finalize its run after takeover.
func TestTenantHealthSchedulerTakeoverFencesLateWorkerWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "integrated-takeover.db")
	oldStore, err := adminstore.Open(context.Background(), path, strings.Repeat("0", 64))
	if err != nil {
		t.Fatal(err)
	}
	defer oldStore.Close()
	newStore, err := adminstore.Open(context.Background(), path, strings.Repeat("0", 64))
	if err != nil {
		t.Fatal(err)
	}
	defer newStore.Close()

	source, err := oldStore.Create(context.Background(), adminstore.DataSourceInput{
		Name: "takeover-source", Driver: "sqlite", DSN: ":memory:",
	})
	if err != nil {
		t.Fatal(err)
	}
	tenantItem, err := oldStore.CreateTenant(context.Background(), adminstore.TenantInput{
		Name: "Takeover tenant", Code: "takeover-tenant", DataSourceID: source.ID,
	})
	if err != nil {
		t.Fatal(err)
	}

	workerStarted := make(chan struct{})
	workerCanceled := make(chan struct{})
	workerWrite := make(chan error, 1)
	oldRunID := make(chan int64, 1)
	oldDone := make(chan error, 1)
	go func() {
		oldDone <- runWithTenantHealthSchedulerLeaseTTL(context.Background(), oldStore, "old-api-instance", 5*time.Second, func(runCtx context.Context) error {
			lease, ok := runCtx.Value(tenantHealthSchedulerLeaseContextKey{}).(tenantHealthSchedulerLeaseContext)
			if !ok {
				return errors.New("old round has no lease context")
			}
			startedAt := time.Now().UTC()
			runID, err := oldStore.StartTenantHealthSchedulerRunWithLease(runCtx, lease.ownerID, lease.generation, startedAt)
			if err != nil {
				return err
			}
			oldRunID <- runID

			workerDone := make(chan struct{})
			go func() {
				close(workerStarted)
				<-runCtx.Done()
				close(workerCanceled)
				// Simulate a worker that ignores cancellation and returns late. The
				// store must reject it through owner/generation lease fencing.
				workerWrite <- oldStore.RecordTenantConnectionHealthWithSchedulerLease(context.Background(), adminstore.TenantConnectionHealth{
					TenantID: tenantItem.ID, Status: "unhealthy", CheckedAt: time.Now().UTC(),
					Message: "late old worker", ErrorCode: "connection_failed",
				}, lease.ownerID, lease.generation)
				close(workerDone)
			}()
			<-workerDone

			finishErr := oldStore.FinishTenantHealthSchedulerRunWithLease(
				context.Background(), lease.ownerID, lease.generation, runID,
				time.Now().UTC(), time.Since(startedAt).Milliseconds(), "succeeded", "",
			)
			return finishErr
		})
	}()

	select {
	case <-workerStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("old API instance did not start its worker")
	}
	runID := <-oldRunID

	if err := oldStore.ReleaseTenantHealthSchedulerLeaseWithGeneration(context.Background(), "old-api-instance", 1); err != nil {
		t.Fatalf("release old lease: %v", err)
	}
	takeoverAt := time.Now().UTC()
	acquired, generation, err := newStore.AcquireTenantHealthSchedulerLease(context.Background(), "new-api-instance", takeoverAt, 5*time.Second)
	if err != nil || !acquired || generation != 2 {
		t.Fatalf("new API instance takeover: acquired=%v generation=%d err=%v", acquired, generation, err)
	}

	select {
	case <-workerCanceled:
	case <-time.After(4 * time.Second):
		t.Fatal("old worker was not canceled after lease takeover")
	}
	select {
	case err := <-workerWrite:
		if !errors.Is(err, adminstore.ErrTenantHealthSchedulerLeaseLost) {
			t.Fatalf("late old worker write error=%v, want lease lost from fencing", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("late old worker write did not return")
	}

	newStarted := time.Now().UTC()
	newRunID, err := newStore.StartTenantHealthSchedulerRunWithLease(context.Background(), "new-api-instance", generation, newStarted)
	if err != nil {
		t.Fatalf("start replacement run: %v", err)
	}
	if err := newStore.RecordTenantConnectionHealthWithSchedulerLease(context.Background(), adminstore.TenantConnectionHealth{
		TenantID: tenantItem.ID, Status: "healthy", CheckedAt: newStarted,
		Message: "replacement instance result",
	}, "new-api-instance", generation); err != nil {
		t.Fatalf("persist replacement health result: %v", err)
	}
	if err := newStore.FinishTenantHealthSchedulerRunWithLease(
		context.Background(), "new-api-instance", generation, newRunID,
		newStarted.Add(time.Millisecond), 1, "succeeded", "",
	); err != nil {
		t.Fatalf("finish replacement run: %v", err)
	}

	select {
	case err := <-oldDone:
		if !errors.Is(err, errTenantHealthSchedulerLeaseLost) {
			t.Fatalf("old API instance result=%v, want lease lost", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("old API instance did not stop after takeover")
	}

	history, err := newStore.ListTenantConnectionHealthHistory(context.Background(), adminstore.TenantHealthHistoryFilter{
		TenantID: tenantItem.ID, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Items) != 1 || history.Items[0].Status != "healthy" || history.Items[0].Source != "scheduled" {
		t.Fatalf("late old worker wrote a stale health history row: %+v", history.Items)
	}
	latest, err := newStore.ListTenantConnectionHealth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(latest) != 1 || latest[0].Status != "healthy" {
		t.Fatalf("late old worker overwrote replacement health snapshot: %+v", latest)
	}
	page, err := newStore.ListTenantHealthSchedulerRuns(context.Background(), adminstore.TenantHealthSchedulerRunFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var oldRunFound, newRunSucceeded bool
	for _, item := range page.Items {
		if item.ID == runID {
			oldRunFound = true
			if item.Status != "running" {
				t.Fatalf("old run status=%q, want running until recovery", item.Status)
			}
		}
		if item.ID == newRunID {
			newRunSucceeded = item.Status == "succeeded"
		}
	}
	if !oldRunFound || !newRunSucceeded {
		t.Fatalf("run history missing expected states: old=%v newSucceeded=%v items=%+v", oldRunFound, newRunSucceeded, page.Items)
	}

	recovered, err := newStore.RecoverStaleTenantHealthSchedulerRunsWithLease(context.Background(), "new-api-instance", generation, time.Now().UTC())
	if err != nil || recovered != 1 {
		t.Fatalf("recover old run: recovered=%d err=%v, want one interrupted run", recovered, err)
	}
	page, err = newStore.ListTenantHealthSchedulerRuns(context.Background(), adminstore.TenantHealthSchedulerRunFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range page.Items {
		if item.ID == runID && item.Status != "interrupted" {
			t.Fatalf("recovered old run status=%q, want interrupted", item.Status)
		}
	}
}


// TestRunWithTenantHealthSchedulerLeaseRecoversAfterMetadataDatabaseReturns
// verifies that a fresh API instance can take over after an outage expires the
// old instance's lease, without running overlapping rounds.
func TestRunWithTenantHealthSchedulerLeaseRecoversAfterMetadataDatabaseReturns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scheduler-recovery.db")
	oldStore, err := adminstore.Open(context.Background(), path, strings.Repeat("0", 64))
	if err != nil {
		t.Fatal(err)
	}

	oldStarted := make(chan struct{})
	oldCanceled := make(chan struct{})
	oldDone := make(chan error, 1)
	go func() {
		oldDone <- runWithTenantHealthSchedulerLeaseTTL(context.Background(), oldStore, "api-instance-before-outage", 5*time.Second, func(runCtx context.Context) error {
			close(oldStarted)
			<-runCtx.Done()
			close(oldCanceled)
			return runCtx.Err()
		})
	}()

	select {
	case <-oldStarted:
	case <-time.After(2 * time.Second):
		_ = oldStore.Close()
		t.Fatal("old scheduler did not acquire the lease")
	}

	outageAt := time.Now().UTC()
	if err := oldStore.Close(); err != nil {
		t.Fatalf("close metadata store during outage: %v", err)
	}

	select {
	case <-oldCanceled:
	case <-time.After(4 * time.Second):
		t.Fatal("old scheduler did not stop after metadata database became unavailable")
	}
	select {
	case err := <-oldDone:
		if !errors.Is(err, errTenantHealthSchedulerLeaseLost) {
			t.Fatalf("old scheduler result=%v, want lease lost", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("old scheduler did not finish after outage")
	}

	// Reopen the same metadata database as a replacement API instance. It must
	// wait for the old lease expiry instead of stealing a still-valid lease.
	newStore, err := adminstore.Open(context.Background(), path, strings.Repeat("0", 64))
	if err != nil {
		t.Fatal(err)
	}
	defer newStore.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Second)
	defer cancel()
	var replacementRuns atomic.Int32
	var replacementGeneration atomic.Int64
	for {
		err = runWithTenantHealthSchedulerLeaseTTL(ctx, newStore, "api-instance-after-recovery", 5*time.Second, func(runCtx context.Context) error {
			lease, ok := runCtx.Value(tenantHealthSchedulerLeaseContextKey{}).(tenantHealthSchedulerLeaseContext)
			if !ok {
				return errors.New("replacement round has no lease context")
			}
			replacementGeneration.Store(lease.generation)
			replacementRuns.Add(1)
			return nil
		})
		if err == nil {
			break
		}
		if !errors.Is(err, errTenantHealthSchedulerLeaseNotAcquired) {
			t.Fatalf("replacement scheduler acquisition failed: %v", err)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("replacement scheduler did not recover before deadline (outage began %s ago)", time.Since(outageAt))
		case <-time.After(100 * time.Millisecond):
		}
	}

	if got := replacementRuns.Load(); got != 1 {
		t.Fatalf("replacement scheduler runs=%d, want exactly one", got)
	}
	if got := replacementGeneration.Load(); got != 2 {
		t.Fatalf("replacement lease generation=%d, want 2", got)
	}

	info, err := newStore.GetTenantHealthSchedulerLeaseInfo(context.Background(), "api-instance-after-recovery", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if info.Generation != 2 || info.Status != "held_by_this_instance" {
		t.Fatalf("recovered lease info=%+v, want replacement owner at generation 2", info)
	}
}
