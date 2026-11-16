package adminstore

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestTenantHealthSchedulerRunLifecycleAndPaging(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 11, 13, 1, 0, 0, 0, time.UTC)

	acquired, firstGeneration, err := store.AcquireTenantHealthSchedulerLease(ctx, "run-owner-one", base, 5*time.Second)
	if err != nil || !acquired { t.Fatalf("first lease acquired=%v err=%v", acquired, err) }
	firstID, err := store.StartTenantHealthSchedulerRunWithLease(ctx, "run-owner-one", firstGeneration, base)
	if err != nil { t.Fatal(err) }
	secondStartedAt := base.Add(time.Minute)
	acquired, secondGeneration, err := store.AcquireTenantHealthSchedulerLease(ctx, "run-owner-two", secondStartedAt, 5*time.Second)
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
	acquired, oldGeneration, err := store.AcquireTenantHealthSchedulerLease(ctx, "recovery-owner-one", base, 5*time.Second)
	if err != nil || !acquired { t.Fatalf("old lease acquired=%v err=%v", acquired, err) }
	oldID, err := store.StartTenantHealthSchedulerRunWithLease(ctx, "recovery-owner-one", oldGeneration, base)
	if err != nil { t.Fatal(err) }
	takeoverAt := base.Add(5*time.Second)
	acquired, currentGeneration, err := store.AcquireTenantHealthSchedulerLease(ctx, "recovery-owner-two", takeoverAt, 5*time.Second)
	if err != nil || !acquired { t.Fatalf("takeover lease acquired=%v err=%v", acquired, err) }
	currentID, err := store.StartTenantHealthSchedulerRunWithLease(ctx, "recovery-owner-two", currentGeneration, takeoverAt)
	if err != nil { t.Fatal(err) }
	recovered, err := store.RecoverStaleTenantHealthSchedulerRuns(ctx, currentGeneration, takeoverAt.Add(time.Second))
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

	acquired, generation, err := store.AcquireTenantHealthSchedulerLease(ctx, "owner-one", base, 5*time.Second)
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

	acquired, nextGeneration, err := store.AcquireTenantHealthSchedulerLease(ctx, "owner-two", base.Add(5*time.Second), 5*time.Second)
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
	for _, tc := range []struct{name, owner string; gen int64; at time.Time}{
		{name:"wrong owner", owner:"retention-owner-b", gen:generation, at:now},
		{name:"wrong generation", owner:"retention-owner-a", gen:generation+1, at:now},
		{name:"expired lease", owner:"retention-owner-a", gen:generation, at:now.Add(6*time.Second)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deleted, err := store.DeleteExpiredTenantHealthSchedulerRunsWithLease(ctx, tc.owner, tc.gen, tc.at, before, 500)
			if deleted != 0 || !errors.Is(err, ErrTenantHealthSchedulerLeaseLost) {
				t.Fatalf("delete result=(%d,%v), want lease lost without deletion", deleted, err)
			}
		})
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
