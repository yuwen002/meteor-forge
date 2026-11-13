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
