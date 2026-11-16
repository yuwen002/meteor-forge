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
