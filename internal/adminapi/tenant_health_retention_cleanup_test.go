package adminapi

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yuwen002/meteor-forge/internal/adminstore"
)

func TestExecuteTenantHealthRetentionCleanupPersistsPartialBatchCounts(t *testing.T) {
	calls := 0
	var gotStatus, gotCode string
	var gotHistory, gotRuns int64
	wantErr := errors.New("second history batch failed")
	err := executeTenantHealthRetentionCleanup(context.Background(), 30,
		func(_ context.Context, _ time.Time, limit int) (int64, error) {
			if limit != 500 { t.Fatalf("history batch limit=%d, want 500", limit) }
			calls++
			if calls == 1 { return 500, nil }
			return 7, wantErr
		},
		func(context.Context, time.Time, int) (int64, error) {
			t.Fatal("run history cleanup must not start after history cleanup fails")
			return 0, nil
		},
		func(_ context.Context, startedAt, finishedAt time.Time, status string, history, runs int64, code string) error {
			if startedAt.IsZero() || finishedAt.Before(startedAt) { t.Fatalf("invalid cleanup timestamps: %v - %v", startedAt, finishedAt) }
			gotStatus, gotCode, gotHistory, gotRuns = status, code, history, runs
			return nil
		},
	)
	if !errors.Is(err, wantErr) { t.Fatalf("cleanup error=%v, want original batch error", err) }
	if gotStatus != "failed" || gotCode != "cleanup_failed" || gotHistory != 507 || gotRuns != 0 {
		t.Fatalf("persisted cleanup summary status=%q code=%q history=%d runs=%d", gotStatus, gotCode, gotHistory, gotRuns)
	}
}

func TestExecuteTenantHealthRetentionCleanupDoesNotPersistAfterLeaseLoss(t *testing.T) {
	var recordCalls int
	err := executeTenantHealthRetentionCleanup(context.Background(), 30,
		func(context.Context, time.Time, int) (int64, error) {
			return 0, adminstore.ErrTenantHealthSchedulerLeaseLost
		},
		func(context.Context, time.Time, int) (int64, error) { return 0, nil },
		func(context.Context, time.Time, time.Time, string, int64, int64, string) error {
			recordCalls++
			return adminstore.ErrTenantHealthSchedulerLeaseLost
		},
	)
	if !errors.Is(err, adminstore.ErrTenantHealthSchedulerLeaseLost) { t.Fatalf("cleanup error=%v, want lease lost", err) }
	if recordCalls != 1 { t.Fatalf("fenced record attempts=%d, want 1", recordCalls) }
}

func TestExecuteTenantHealthRetentionCleanupReturnsRecordFailure(t *testing.T) {
	recordErr := errors.New("metadata database unavailable")
	err := executeTenantHealthRetentionCleanup(context.Background(), 30,
		func(context.Context, time.Time, int) (int64, error) { return 0, nil },
		func(context.Context, time.Time, int) (int64, error) { return 0, nil },
		func(context.Context, time.Time, time.Time, string, int64, int64, string) error { return recordErr },
	)
	if !errors.Is(err, recordErr) { t.Fatalf("cleanup error=%v, want record failure", err) }
}


func TestExecuteTenantHealthRetentionCleanupPersistsBothQueueCounts(t *testing.T) {
	historyCalls, runCalls := 0, 0
	var gotStatus, gotCode string
	var gotHistory, gotRuns int64
	err := executeTenantHealthRetentionCleanup(context.Background(), 30,
		func(_ context.Context, _ time.Time, limit int) (int64, error) {
			if limit != 500 { t.Fatalf("history batch limit=%d, want 500", limit) }
			historyCalls++
			if historyCalls == 1 { return 500, nil }
			return 3, nil
		},
		func(_ context.Context, _ time.Time, limit int) (int64, error) {
			if limit != 500 { t.Fatalf("run batch limit=%d, want 500", limit) }
			runCalls++
			return 2, nil
		},
		func(_ context.Context, startedAt, finishedAt time.Time, status string, history, runs int64, code string) error {
			if startedAt.IsZero() || finishedAt.Before(startedAt) { t.Fatalf("invalid cleanup timestamps: %v - %v", startedAt, finishedAt) }
			gotStatus, gotCode, gotHistory, gotRuns = status, code, history, runs
			return nil
		},
	)
	if err != nil { t.Fatalf("cleanup error=%v, want nil", err) }
	if historyCalls != 2 || runCalls != 1 { t.Fatalf("batch calls history=%d runs=%d, want 2 and 1", historyCalls, runCalls) }
	if gotStatus != "succeeded" || gotCode != "" || gotHistory != 503 || gotRuns != 2 {
		t.Fatalf("persisted cleanup summary status=%q code=%q history=%d runs=%d", gotStatus, gotCode, gotHistory, gotRuns)
	}
}

func TestExecuteTenantHealthRetentionCleanupJoinsCleanupAndRecordFailures(t *testing.T) {
	cleanupErr := errors.New("history cleanup failed")
	recordErr := errors.New("cleanup status write failed")
	err := executeTenantHealthRetentionCleanup(context.Background(), 30,
		func(context.Context, time.Time, int) (int64, error) { return 0, cleanupErr },
		func(context.Context, time.Time, int) (int64, error) {
			t.Fatal("run history cleanup must not start after history cleanup fails")
			return 0, nil
		},
		func(context.Context, time.Time, time.Time, string, int64, int64, string) error { return recordErr },
	)
	if !errors.Is(err, cleanupErr) || !errors.Is(err, recordErr) {
		t.Fatalf("cleanup error=%v, want both cleanup and record errors", err)
	}
}


func TestExecuteTenantHealthRetentionCleanupStopsAfterContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	historyCalls := 0
	var gotStatus, gotCode string
	var gotHistory int64
	err := executeTenantHealthRetentionCleanup(ctx, 30,
		func(context.Context, time.Time, int) (int64, error) {
			historyCalls++
			if historyCalls == 1 {
				cancel()
				return 500, nil
			}
			t.Fatal("cleanup started another batch after context cancellation")
			return 0, nil
		},
		func(context.Context, time.Time, int) (int64, error) {
			t.Fatal("run history cleanup started after context cancellation")
			return 0, nil
		},
		func(recordCtx context.Context, _ time.Time, _ time.Time, status string, history, _ int64, code string) error {
			if recordCtx.Err() != nil {
				t.Fatalf("record context error=%v, want independent active context", recordCtx.Err())
			}
			deadline, ok := recordCtx.Deadline()
			if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 2*time.Second {
				t.Fatalf("record context deadline=%v present=%v, want a deadline within 2 seconds", deadline, ok)
			}
			gotStatus, gotHistory, gotCode = status, history, code
			return nil
		},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cleanup error=%v, want context.Canceled", err)
	}
	if historyCalls != 1 || gotStatus != "failed" || gotCode != "cleanup_failed" || gotHistory != 500 {
		t.Fatalf("calls=%d status=%q code=%q history=%d", historyCalls, gotStatus, gotCode, gotHistory)
	}
}
