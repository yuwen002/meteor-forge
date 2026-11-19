package adminstore

import (
	"context"
	"errors"
	"testing"
	"time"
	"path/filepath"
)

func TestTenantHealthSchedulerRunLifecycleAndPaging(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 11, 13, 1, 0, 0, 0, time.UTC)

	acquired, firstGeneration, err := store.acquireTenantHealthSchedulerLeaseAt(ctx, "run-owner-one", base, 5*time.Second)
	if err != nil || !acquired { t.Fatalf("first lease acquired=%v err=%v", acquired, err) }
	firstID, err := store.StartTenantHealthSchedulerRunWithLease(ctx, "run-owner-one", firstGeneration, base)
	if err != nil { t.Fatal(err) }
	secondStartedAt := base.Add(time.Minute)
	acquired, secondGeneration, err := store.acquireTenantHealthSchedulerLeaseAt(ctx, "run-owner-two", secondStartedAt, 5*time.Second)
	if err != nil || !acquired { t.Fatalf("second lease acquired=%v err=%v", acquired, err) }
	secondID, err := store.StartTenantHealthSchedulerRunWithLease(ctx, "run-owner-two", secondGeneration, secondStartedAt)
	if err != nil { t.Fatal(err) }
	if err := store.FinishTenantHealthSchedulerRun(ctx, firstID, base.Add(10*time.Second), 10000, "failed", "tenant_checks_failed"); err != nil { t.Fatal(err) }
	if err := store.FinishTenantHealthSchedulerRun(ctx, secondID, base.Add(70*time.Second), 10000, "succeeded", ""); err != nil { t.Fatal(err) }
	if err := store.FinishTenantHealthSchedulerRun(ctx, secondID, base.Add(71*time.Second), 11000, "succeeded", ""); !errors.Is(err, ErrTenantHealthSchedulerRunFinalized) {
		t.Fatalf("second finalization error=%v", err)
	}

	page, err := store.ListTenantHealthSchedulerRuns(ctx, TenantHealthSchedulerRunFilter{Limit: 1})
	if err != nil { t.Fatal(err) }
	if page.Total != 2 || len(page.Items) != 1 || page.Items[0].ID != secondID || page.Items[0].Status != "succeeded" || page.Items[0].FinishedAt == nil {
		t.Fatalf("unexpected newest run page: %+v", page)
	}
	failed, err := store.ListTenantHealthSchedulerRuns(ctx, TenantHealthSchedulerRunFilter{Status: "failed"})
	if err != nil { t.Fatal(err) }
	if failed.Total != 1 || len(failed.Items) != 1 || failed.Items[0].ErrorCode != "tenant_checks_failed" || failed.Items[0].ErrorMessage != "一个或多个租户巡检失败" {
		t.Fatalf("failed run was not safely classified: %+v", failed)
	}
	if failed.Summary.Total != 2 || failed.Summary.Succeeded != 1 || failed.Summary.Failed != 1 || failed.Summary.AverageDurationMS != 10000 {
		t.Fatalf("status filter must not narrow time-range summary: %+v", failed.Summary)
	}
	from := secondStartedAt
	filteredByTime, err := store.ListTenantHealthSchedulerRuns(ctx, TenantHealthSchedulerRunFilter{Status: "failed", From: &from})
	if err != nil { t.Fatal(err) }
	if filteredByTime.Total != 0 || filteredByTime.Summary.Total != 1 || filteredByTime.Summary.Succeeded != 1 {
		t.Fatalf("time-range summary should ignore status filter: %+v", filteredByTime)
	}
}

func TestTenantHealthSchedulerRunRecoveryAndValidation(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 11, 13, 2, 0, 0, 0, time.UTC)
	acquired, oldGeneration, err := store.acquireTenantHealthSchedulerLeaseAt(ctx, "recovery-owner-one", base, 5*time.Second)
	if err != nil || !acquired { t.Fatalf("old lease acquired=%v err=%v", acquired, err) }
	oldID, err := store.StartTenantHealthSchedulerRunWithLease(ctx, "recovery-owner-one", oldGeneration, base)
	if err != nil { t.Fatal(err) }
	takeoverAt := base.Add(5*time.Second)
	acquired, currentGeneration, err := store.acquireTenantHealthSchedulerLeaseAt(ctx, "recovery-owner-two", takeoverAt, 5*time.Second)
	if err != nil || !acquired { t.Fatalf("takeover lease acquired=%v err=%v", acquired, err) }
	currentID, err := store.StartTenantHealthSchedulerRunWithLease(ctx, "recovery-owner-two", currentGeneration, takeoverAt)
	if err != nil { t.Fatal(err) }
	if _, err := store.recoverStaleTenantHealthSchedulerRunsWithLeaseClock(ctx, "recovery-owner-one", currentGeneration, func() time.Time { return takeoverAt.Add(time.Second) }); !errors.Is(err, ErrTenantHealthSchedulerLeaseLost) {
		t.Fatalf("stale owner recovery error=%v, want lease lost", err)
	}
	if _, err := store.recoverStaleTenantHealthSchedulerRunsWithLeaseClock(ctx, "recovery-owner-two", currentGeneration+1, func() time.Time { return takeoverAt.Add(time.Second) }); !errors.Is(err, ErrTenantHealthSchedulerLeaseLost) {
		t.Fatalf("wrong generation recovery error=%v, want lease lost", err)
	}
	recovered, err := store.recoverStaleTenantHealthSchedulerRunsWithLeaseClock(ctx, "recovery-owner-two", currentGeneration, func() time.Time { return takeoverAt.Add(time.Second) })
	if err != nil { t.Fatal(err) }
	if recovered != 1 { t.Fatalf("recovered=%d, want 1", recovered) }
	page, err := store.ListTenantHealthSchedulerRuns(ctx, TenantHealthSchedulerRunFilter{})
	if err != nil { t.Fatal(err) }
	statuses := map[int64]string{}
	for _, item := range page.Items { statuses[item.ID] = item.Status }
	if statuses[oldID] != "interrupted" || statuses[currentID] != "running" {
		t.Fatalf("unexpected recovered run states: %+v", statuses)
	}
	for _, item := range page.Items {
		if item.ID == oldID {
			wantDuration := takeoverAt.Add(time.Second).Sub(base).Milliseconds()
			if item.FinishedAt == nil || !item.FinishedAt.Equal(takeoverAt.Add(time.Second)) || item.DurationMS != wantDuration {
				t.Fatalf("recovered run should preserve finish time and elapsed duration: %+v, want duration %d", item, wantDuration)
			}
		}
	}
	if _, err := store.ListTenantHealthSchedulerRuns(ctx, TenantHealthSchedulerRunFilter{Status: "invalid"}); !errors.Is(err, ErrTenantHealthInvalidInput) {
		t.Fatalf("invalid status error=%v", err)
	}
	if _, err := store.ListTenantHealthSchedulerRuns(ctx, TenantHealthSchedulerRunFilter{Offset: -1}); !errors.Is(err, ErrTenantHealthInvalidInput) {
		t.Fatalf("invalid offset error=%v", err)
	}
}

func TestTenantHealthSchedulerRunStartRequiresCurrentLease(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 11, 13, 3, 0, 0, 0, time.UTC)

	acquired, generation, err := store.acquireTenantHealthSchedulerLeaseAt(ctx, "owner-one", base, 5*time.Second)
	if err != nil || !acquired || generation != 1 {
		t.Fatalf("first lease acquired=%v generation=%d err=%v", acquired, generation, err)
	}
	if _, err := store.StartTenantHealthSchedulerRunWithLease(ctx, "owner-two", generation, base.Add(time.Second)); !errors.Is(err, ErrTenantHealthSchedulerLeaseLost) {
		t.Fatalf("wrong owner start error=%v", err)
	}
	if _, err := store.StartTenantHealthSchedulerRunWithLease(ctx, "owner-one", generation+1, base.Add(time.Second)); !errors.Is(err, ErrTenantHealthSchedulerLeaseLost) {
		t.Fatalf("wrong generation start error=%v", err)
	}
	if _, err := store.StartTenantHealthSchedulerRunWithLease(ctx, "owner-one", generation, base.Add(5*time.Second)); !errors.Is(err, ErrTenantHealthSchedulerLeaseLost) {
		t.Fatalf("expired lease start error=%v", err)
	}

	acquired, nextGeneration, err := store.acquireTenantHealthSchedulerLeaseAt(ctx, "owner-two", base.Add(5*time.Second), 5*time.Second)
	if err != nil || !acquired || nextGeneration != generation+1 {
		t.Fatalf("takeover acquired=%v generation=%d err=%v", acquired, nextGeneration, err)
	}
	id, err := store.StartTenantHealthSchedulerRunWithLease(ctx, "owner-two", nextGeneration, base.Add(5*time.Second))
	if err != nil || id <= 0 {
		t.Fatalf("current owner start id=%d err=%v", id, err)
	}
	if _, err := store.ListTenantHealthSchedulerRuns(ctx, TenantHealthSchedulerRunFilter{}); err != nil {
		t.Fatal(err)
	}
}


func TestTenantHealthSchedulerRunRetentionDeletesOnlyExpiredFinalizedRowsInBatches(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	acquired, generation, err := store.AcquireTenantHealthSchedulerLease(ctx, "retention-owner", now, 5*time.Minute)
	if err != nil || !acquired { t.Fatalf("acquire retention lease: acquired=%v err=%v", acquired, err) }
	createRun := func(startedAt, finishedAt time.Time, finalize bool) int64 {
		t.Helper()
		id, err := store.StartTenantHealthSchedulerRunWithLease(ctx, "retention-owner", generation, startedAt)
		if err != nil { t.Fatalf("start run: %v", err) }
		if finalize {
			if err := store.FinishTenantHealthSchedulerRun(ctx, id, finishedAt, 1000, "succeeded", ""); err != nil { t.Fatalf("finish run: %v", err) }
		}
		return id
	}
	oldID := createRun(now.Add(-40*24*time.Hour), now.Add(-31*24*time.Hour), true)
	boundaryID := createRun(now.Add(-31*24*time.Hour), now.Add(-30*24*time.Hour), true)
	recentID := createRun(now.Add(-2*24*time.Hour), now.Add(-24*time.Hour), true)
	runningID := createRun(now.Add(-40*24*time.Hour), time.Time{}, false)
	before := now.Add(-30*24*time.Hour)
	deleted, err := store.DeleteExpiredTenantHealthSchedulerRunsWithLease(ctx, "retention-owner", generation, now, before, 500)
	if err != nil { t.Fatal(err) }
	if deleted != 1 { t.Fatalf("deleted=%d, want only one expired finalized row", deleted) }
	page, err := store.ListTenantHealthSchedulerRuns(ctx, TenantHealthSchedulerRunFilter{Limit: 100})
	if err != nil { t.Fatal(err) }
	states := make(map[int64]string, len(page.Items))
	for _, item := range page.Items { states[item.ID] = item.Status }
	if _, exists := states[oldID]; exists { t.Fatalf("expired run %d was not deleted", oldID) }
	if states[boundaryID] != "succeeded" || states[recentID] != "succeeded" || states[runningID] != "running" {
		t.Fatalf("retention removed a boundary, recent, or running record: %+v", states)
	}
}

func TestTenantHealthSchedulerRunRetentionRequiresCurrentUnexpiredLease(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	acquired, generation, err := store.AcquireTenantHealthSchedulerLease(ctx, "retention-owner-a", now, 5*time.Second)
	if err != nil || !acquired { t.Fatalf("acquire lease: acquired=%v err=%v", acquired, err) }
	id, err := store.StartTenantHealthSchedulerRunWithLease(ctx, "retention-owner-a", generation, now.Add(-48*time.Hour))
	if err != nil { t.Fatal(err) }
	if err := store.FinishTenantHealthSchedulerRun(ctx, id, now.Add(-24*time.Hour), 1000, "succeeded", ""); err != nil { t.Fatal(err) }
	before := now.Add(-12*time.Hour)
	for _, tc := range []struct{name, owner string; gen int64}{
		{name:"wrong owner", owner:"retention-owner-b", gen:generation},
		{name:"wrong generation", owner:"retention-owner-a", gen:generation+1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deleted, err := store.DeleteExpiredTenantHealthSchedulerRunsWithLease(ctx, tc.owner, tc.gen, now, before, 500)
			if deleted != 0 || !errors.Is(err, ErrTenantHealthSchedulerLeaseLost) {
				t.Fatalf("delete result=(%d,%v), want lease lost without deletion", deleted, err)
			}
		})
	}
	// A stale caller timestamp must not authorize deletion after the stored lease expires.
	if _, err := store.db.ExecContext(ctx, "UPDATE admin_tenant_health_scheduler_lease SET lease_until = ? WHERE id = 1", time.Now().UTC().Add(-time.Second).UnixNano()); err != nil {
		t.Fatal(err)
	}
	if deleted, err := store.DeleteExpiredTenantHealthSchedulerRunsWithLease(ctx, "retention-owner-a", generation, now, before, 500); deleted != 0 || !errors.Is(err, ErrTenantHealthSchedulerLeaseLost) {
		t.Fatalf("expired lease delete result=(%d,%v), want lease lost without deletion", deleted, err)
	}
	page, err := store.ListTenantHealthSchedulerRuns(ctx, TenantHealthSchedulerRunFilter{})
	if err != nil { t.Fatal(err) }
	if page.Total != 1 || page.Items[0].ID != id { t.Fatalf("invalid lease deleted history: %+v", page) }
}

func TestTenantHealthSchedulerRunRetentionHonorsBatchLimit(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	acquired, generation, err := store.AcquireTenantHealthSchedulerLease(ctx, "retention-batch-owner", now, 5*time.Minute)
	if err != nil || !acquired { t.Fatalf("acquire lease: acquired=%v err=%v", acquired, err) }
	for i := 0; i < 3; i++ {
		started := now.Add(time.Duration(i-10)*24*time.Hour)
		id, err := store.StartTenantHealthSchedulerRunWithLease(ctx, "retention-batch-owner", generation, started)
		if err != nil { t.Fatal(err) }
		if err := store.FinishTenantHealthSchedulerRun(ctx, id, started.Add(time.Hour), 1000, "succeeded", ""); err != nil { t.Fatal(err) }
	}
	before := now.Add(-5*24*time.Hour)
	deleted, err := store.DeleteExpiredTenantHealthSchedulerRunsWithLease(ctx, "retention-batch-owner", generation, now, before, 2)
	if err != nil || deleted != 2 { t.Fatalf("first batch deleted=%d err=%v, want 2", deleted, err) }
	deleted, err = store.DeleteExpiredTenantHealthSchedulerRunsWithLease(ctx, "retention-batch-owner", generation, now, before, 2)
	if err != nil || deleted != 1 { t.Fatalf("second batch deleted=%d err=%v, want 1", deleted, err) }
}


func TestTenantHealthSchedulerRunFinishRequiresCurrentLeaseGeneration(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 11, 18, 1, 0, 0, 0, time.UTC)

	acquired, oldGeneration, err := store.acquireTenantHealthSchedulerLeaseAt(ctx, "finish-owner", base, 5*time.Second)
	if err != nil || !acquired || oldGeneration != 1 {
		t.Fatalf("acquire old lease: acquired=%v generation=%d err=%v", acquired, oldGeneration, err)
	}
	runID, err := store.StartTenantHealthSchedulerRunWithLease(ctx, "finish-owner", oldGeneration, base)
	if err != nil { t.Fatal(err) }

	for _, tc := range []struct{name, owner string; generation int64}{
		{name: "wrong owner", owner: "other-owner", generation: oldGeneration},
		{name: "wrong generation", owner: "finish-owner", generation: oldGeneration + 1},
	} {
		err := store.FinishTenantHealthSchedulerRunWithLease(
			ctx, tc.owner, tc.generation, runID, base.Add(time.Second), 1000, "succeeded", "",
		)
		if !errors.Is(err, ErrTenantHealthSchedulerLeaseLost) {
			t.Fatalf("%s finish error=%v, want lease lost", tc.name, err)
		}
	}

	takeoverAt := base.Add(5 * time.Second)
	acquired, newGeneration, err := store.acquireTenantHealthSchedulerLeaseAt(ctx, "replacement-owner", takeoverAt, 5*time.Second)
	if err != nil || !acquired || newGeneration != oldGeneration+1 {
		t.Fatalf("takeover: acquired=%v generation=%d err=%v", acquired, newGeneration, err)
	}
	if err := store.FinishTenantHealthSchedulerRunWithLease(
		ctx, "finish-owner", oldGeneration, runID, takeoverAt.Add(time.Second), 6000, "succeeded", "",
	); !errors.Is(err, ErrTenantHealthSchedulerLeaseLost) {
		t.Fatalf("stale generation finish error=%v, want lease lost", err)
	}
	if err := store.FinishTenantHealthSchedulerRunWithLease(
		ctx, "replacement-owner", newGeneration, runID, takeoverAt.Add(time.Second), 6000, "succeeded", "",
	); !errors.Is(err, ErrTenantHealthSchedulerLeaseLost) {
		t.Fatalf("new owner finishing previous-generation run error=%v, want lease lost", err)
	}

	page, err := store.ListTenantHealthSchedulerRuns(ctx, TenantHealthSchedulerRunFilter{})
	if err != nil { t.Fatal(err) }
	if page.Total != 1 || page.Items[0].Status != "running" || page.Items[0].FinishedAt != nil {
		t.Fatalf("stale owner changed run after takeover: %+v", page)
	}

	recovered, err := store.recoverStaleTenantHealthSchedulerRunsWithLeaseClock(
		ctx, "replacement-owner", newGeneration, func() time.Time { return takeoverAt.Add(time.Second) },
	)
	if err != nil || recovered != 1 {
		t.Fatalf("replacement recovery: recovered=%d err=%v", recovered, err)
	}
	page, err = store.ListTenantHealthSchedulerRuns(ctx, TenantHealthSchedulerRunFilter{})
	if err != nil { t.Fatal(err) }
	if page.Items[0].Status != "interrupted" {
		t.Fatalf("stale run should be recovered as interrupted: %+v", page.Items[0])
	}
}


// TestTenantHealthSchedulerRunFinishSamplesLeaseTimeAfterWriteLock verifies
// that a finish request waiting on SQLite cannot use a stale pre-lock timestamp
// to finalize a run after its lease has expired.
func TestTenantHealthSchedulerRunFinishSamplesLeaseTimeAfterWriteLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "finish-lock-expiry.db")
	first, err := Open(context.Background(), path, testKey)
	if err != nil { t.Fatal(err) }
	defer first.Close()
	first.db.SetMaxOpenConns(1)
	if _, err := first.db.ExecContext(context.Background(), "PRAGMA busy_timeout = 10000"); err != nil {
		t.Fatal(err)
	}
	second, err := Open(context.Background(), path, testKey)
	if err != nil { t.Fatal(err) }
	defer second.Close()

	startedAt := time.Now().UTC()
	acquired, generation, err := first.AcquireTenantHealthSchedulerLease(context.Background(), "finish-lock-owner", startedAt, 5*time.Second)
	if err != nil || !acquired { t.Fatalf("acquire lease: acquired=%v err=%v", acquired, err) }
	runID, err := first.StartTenantHealthSchedulerRunWithLease(context.Background(), "finish-lock-owner", generation, startedAt)
	if err != nil { t.Fatal(err) }

	lockTx, err := second.db.BeginTx(context.Background(), nil)
	if err != nil { t.Fatal(err) }
	if _, err := lockTx.ExecContext(context.Background(), "UPDATE admin_tenant_health_scheduler_lease SET updated_at = updated_at WHERE id = 1"); err != nil {
		_ = lockTx.Rollback()
		t.Fatal(err)
	}

	finishResult := make(chan error, 1)
	go func() {
		finishResult <- first.FinishTenantHealthSchedulerRunWithLease(
			context.Background(), "finish-lock-owner", generation, runID,
			time.Now().UTC(), 1000, "succeeded", "",
		)
	}()

	// The 1-second lease expires while finalization is blocked behind the
	// independent connection's write transaction. The wait remains below
	// SQLite's 5-second busy timeout so the test observes lease expiry rather
	// than SQLITE_BUSY.
	time.Sleep(5500 * time.Millisecond)
	if err := lockTx.Rollback(); err != nil { t.Fatal(err) }
	select {
	case err := <-finishResult:
		if !errors.Is(err, ErrTenantHealthSchedulerLeaseLost) {
			t.Fatalf("finish after lock wait error=%v, want lease lost", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("finish did not return after releasing SQLite write lock")
	}

	page, err := first.ListTenantHealthSchedulerRuns(context.Background(), TenantHealthSchedulerRunFilter{})
	if err != nil { t.Fatal(err) }
	if page.Total != 1 || page.Items[0].Status != "running" || page.Items[0].FinishedAt != nil {
		t.Fatalf("expired lease finalized run after lock wait: %+v", page)
	}
}


// TestTenantHealthSchedulerRunRetentionSamplesLeaseTimeAfterWriteLock ensures
// a cleanup blocked on SQLite's writer lock cannot use a stale timestamp to
// delete history after its lease expires.
func TestTenantHealthSchedulerRunRetentionSamplesLeaseTimeAfterWriteLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "retention-lock-expiry.db")
	first, err := Open(context.Background(), path, testKey)
	if err != nil { t.Fatal(err) }
	defer first.Close()
	first.db.SetMaxOpenConns(1)
	if _, err := first.db.ExecContext(context.Background(), "PRAGMA busy_timeout = 10000"); err != nil {
		t.Fatal(err)
	}
	second, err := Open(context.Background(), path, testKey)
	if err != nil { t.Fatal(err) }
	defer second.Close()

	now := time.Now().UTC()
	acquired, generation, err := first.AcquireTenantHealthSchedulerLease(context.Background(), "retention-lock-owner", now, 5*time.Second)
	if err != nil || !acquired { t.Fatalf("acquire lease: acquired=%v err=%v", acquired, err) }
	runID, err := first.StartTenantHealthSchedulerRunWithLease(context.Background(), "retention-lock-owner", generation, now.Add(-48*time.Hour))
	if err != nil { t.Fatal(err) }
	if err := first.FinishTenantHealthSchedulerRun(context.Background(), runID, now.Add(-24*time.Hour), 1000, "succeeded", ""); err != nil {
		t.Fatal(err)
	}

	lockTx, err := second.db.BeginTx(context.Background(), nil)
	if err != nil { t.Fatal(err) }
	if _, err := lockTx.ExecContext(context.Background(), "UPDATE admin_tenant_health_scheduler_lease SET updated_at = updated_at WHERE id = 1"); err != nil {
		_ = lockTx.Rollback()
		t.Fatal(err)
	}

	cleanupResult := make(chan error, 1)
	before := now.Add(-12 * time.Hour)
	go func() {
		_, err := first.DeleteExpiredTenantHealthSchedulerRunsWithLease(
			context.Background(), "retention-lock-owner", generation, now, before, 500,
		)
		cleanupResult <- err
	}()

	// Keep the lock beyond the one-second lease but below SQLite's five-second
	// busy timeout, so the assertion exercises lease expiry rather than SQLITE_BUSY.
	time.Sleep(5500 * time.Millisecond)
	if err := lockTx.Rollback(); err != nil { t.Fatal(err) }
	select {
	case err := <-cleanupResult:
		if !errors.Is(err, ErrTenantHealthSchedulerLeaseLost) {
			t.Fatalf("cleanup after lock wait error=%v, want lease lost", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup did not return after releasing SQLite write lock")
	}

	page, err := first.ListTenantHealthSchedulerRuns(context.Background(), TenantHealthSchedulerRunFilter{})
	if err != nil { t.Fatal(err) }
	if page.Total != 1 || page.Items[0].ID != runID {
		t.Fatalf("expired lease deleted scheduler history after lock wait: %+v", page)
	}
}


// TestTenantHealthSchedulerRunRecoverySamplesLeaseTimeAfterWriteLock verifies
// that recovery cannot use a stale caller timestamp after waiting for SQLite.
func TestTenantHealthSchedulerRunRecoverySamplesLeaseTimeAfterWriteLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recovery-lock-expiry.db")
	first, err := Open(context.Background(), path, testKey)
	if err != nil { t.Fatal(err) }
	defer first.Close()
	first.db.SetMaxOpenConns(1)
	if _, err := first.db.ExecContext(context.Background(), "PRAGMA busy_timeout = 10000"); err != nil { t.Fatal(err) }
	second, err := Open(context.Background(), path, testKey)
	if err != nil { t.Fatal(err) }
	defer second.Close()

	startedAt := time.Now().UTC()
	acquired, oldGeneration, err := first.AcquireTenantHealthSchedulerLease(context.Background(), "recovery-lock-old", startedAt, 5*time.Second)
	if err != nil || !acquired { t.Fatalf("acquire old lease: acquired=%v err=%v", acquired, err) }
	runID, err := first.StartTenantHealthSchedulerRunWithLease(context.Background(), "recovery-lock-old", oldGeneration, startedAt)
	if err != nil { t.Fatal(err) }
	if err := first.ReleaseTenantHealthSchedulerLeaseWithGeneration(context.Background(), "recovery-lock-old", oldGeneration); err != nil { t.Fatal(err) }
	takeoverAt := time.Now().UTC()
	acquired, newGeneration, err := second.AcquireTenantHealthSchedulerLease(context.Background(), "recovery-lock-new", takeoverAt, 5*time.Second)
	if err != nil || !acquired { t.Fatalf("acquire replacement lease: acquired=%v err=%v", acquired, err) }

	lockTx, err := second.db.BeginTx(context.Background(), nil)
	if err != nil { t.Fatal(err) }
	if _, err := lockTx.ExecContext(context.Background(), "UPDATE admin_tenant_health_scheduler_lease SET updated_at = updated_at WHERE id = 1"); err != nil {
		_ = lockTx.Rollback()
		t.Fatal(err)
	}
	result := make(chan error, 1)
	staleCallerTime := time.Now().UTC()
	go func() {
		_, err := first.RecoverStaleTenantHealthSchedulerRunsWithLease(
			context.Background(), "recovery-lock-new", newGeneration, staleCallerTime,
		)
		result <- err
	}()
	time.Sleep(5500 * time.Millisecond)
	if err := lockTx.Rollback(); err != nil { t.Fatal(err) }
	select {
	case err := <-result:
		if !errors.Is(err, ErrTenantHealthSchedulerLeaseLost) {
			t.Fatalf("recovery after lock wait error=%v, want lease lost", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("recovery did not return after releasing SQLite write lock")
	}
	page, err := first.ListTenantHealthSchedulerRuns(context.Background(), TenantHealthSchedulerRunFilter{})
	if err != nil { t.Fatal(err) }
	if page.Total != 1 || page.Items[0].ID != runID || page.Items[0].Status != "running" || page.Items[0].FinishedAt != nil {
		t.Fatalf("expired lease recovery mutated stale run: %+v", page)
	}
}


// TestTenantHealthSchedulerRunStartSamplesLeaseTimeAfterWriteLock verifies
// that a stale caller timestamp cannot authorize a run after lease expiry.
func TestTenantHealthSchedulerRunStartSamplesLeaseTimeAfterWriteLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run-start-lock-expiry.db")
	first, err := Open(context.Background(), path, testKey)
	if err != nil { t.Fatal(err) }
	defer first.Close()
	first.db.SetMaxOpenConns(1)
	if _, err := first.db.ExecContext(context.Background(), "PRAGMA busy_timeout = 10000"); err != nil { t.Fatal(err) }
	second, err := Open(context.Background(), path, testKey)
	if err != nil { t.Fatal(err) }
	defer second.Close()

	startedAt := time.Now().UTC()
	acquired, generation, err := first.AcquireTenantHealthSchedulerLease(context.Background(), "run-start-lock-owner", startedAt, 5*time.Second)
	if err != nil || !acquired { t.Fatalf("acquire lease: acquired=%v err=%v", acquired, err) }

	lockTx, err := second.db.BeginTx(context.Background(), nil)
	if err != nil { t.Fatal(err) }
	if _, err := lockTx.ExecContext(context.Background(), "UPDATE admin_tenant_health_scheduler_lease SET updated_at = updated_at WHERE id = 1"); err != nil {
		_ = lockTx.Rollback()
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := first.StartTenantHealthSchedulerRunWithLease(
			context.Background(), "run-start-lock-owner", generation, startedAt,
		)
		result <- err
	}()
	time.Sleep(5500 * time.Millisecond)
	if err := lockTx.Rollback(); err != nil { t.Fatal(err) }
	select {
	case err := <-result:
		if !errors.Is(err, ErrTenantHealthSchedulerLeaseLost) {
			t.Fatalf("run start after lock wait error=%v, want lease lost", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run start did not return after releasing SQLite write lock")
	}
	page, err := first.ListTenantHealthSchedulerRuns(context.Background(), TenantHealthSchedulerRunFilter{})
	if err != nil { t.Fatal(err) }
	if page.Total != 0 {
		t.Fatalf("expired lease inserted scheduler run: %+v", page)
	}
}
