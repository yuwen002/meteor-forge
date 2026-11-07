package adminapi

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yuwen002/meteor-forge/internal/adminstore"
)

func TestTenantHealthSchedulerDisabledUntilEnabled(t *testing.T) {
	var enabled atomic.Bool
	var runs atomic.Int32
	scheduler := newTenantHealthScheduler(
		func(context.Context) (adminstore.TenantHealthSettings, error) {
			return adminstore.TenantHealthSettings{Enabled: enabled.Load(), IntervalSeconds: 1, HistoryRetentionDays: 30}, nil
		},
		func(context.Context, adminstore.TenantHealthSettings) error { runs.Add(1); return nil },
		func(context.Context, int) error { return nil },
	)
	scheduler.Start(context.Background())
	time.Sleep(30 * time.Millisecond)
	if got := runs.Load(); got != 0 { t.Fatalf("disabled scheduler ran %d times", got) }
	enabled.Store(true)
	scheduler.Refresh()
	time.Sleep(1100 * time.Millisecond)
	scheduler.Close()
	if got := runs.Load(); got != 1 { t.Fatalf("enabled scheduler ran %d times, want 1", got) }
}

func TestTenantHealthSchedulerDoesNotOverlapRoundsAndStops(t *testing.T) {
	var active, maxActive, runs atomic.Int32
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	scheduler := newTenantHealthScheduler(
		func(context.Context) (adminstore.TenantHealthSettings, error) {
			return adminstore.TenantHealthSettings{Enabled: true, IntervalSeconds: 1, HistoryRetentionDays: 30}, nil
		},
		func(ctx context.Context, _ adminstore.TenantHealthSettings) error {
			current := active.Add(1)
			for {
				old := maxActive.Load()
				if current <= old || maxActive.CompareAndSwap(old, current) { break }
			}
			runs.Add(1)
			started <- struct{}{}
			defer active.Add(-1)
			select { case <-release: return nil; case <-ctx.Done(): return ctx.Err() }
		},
		func(context.Context, int) error { return nil },
	)
	scheduler.Start(context.Background())
	select { case <-started: case <-time.After(1500*time.Millisecond): t.Fatal("first round did not start") }
	time.Sleep(1100*time.Millisecond)
	if got := runs.Load(); got != 1 { t.Fatalf("overlapping round started: runs=%d", got) }
	scheduler.Close()
	if got := active.Load(); got != 0 { t.Fatalf("scheduler close returned with %d active rounds", got) }
	close(release)
	if got := maxActive.Load(); got != 1 { t.Fatalf("max active rounds=%d, want 1", got) }
}


func TestCleanupTenantHealthHistoryDrainsBatchesAndStopsOnError(t *testing.T) {
	calls := 0
	err := cleanupTenantHealthHistory(context.Background(), func(_ context.Context, before time.Time, limit int) (int64, error) {
		calls++
		if before.IsZero() || limit != 500 {
			t.Fatalf("unexpected cleanup arguments: before=%v limit=%d", before, limit)
		}
		switch calls {
		case 1, 2:
			return 500, nil
		case 3:
			return 17, nil
		default:
			t.Fatalf("unexpected extra cleanup call: %d", calls)
			return 0, nil
		}
	}, 30)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("cleanup batches=%d, want 3", calls)
	}

	wantErr := context.DeadlineExceeded
	calls = 0
	err = cleanupTenantHealthHistory(context.Background(), func(context.Context, time.Time, int) (int64, error) {
		calls++
		return 0, wantErr
	}, 30)
	if err != wantErr || calls != 1 {
		t.Fatalf("cleanup error=%v calls=%d, want %v and one call", err, calls, wantErr)
	}
}


func TestTenantHealthSchedulerReportsRunAndCleanupErrors(t *testing.T) {
	runErr := errors.New("tenant checks failed")
	cleanupErr := errors.New("history cleanup failed")
	reported := make(chan error, 2)
	scheduler := newTenantHealthScheduler(
		func(context.Context) (adminstore.TenantHealthSettings, error) {
			return adminstore.TenantHealthSettings{Enabled: true, IntervalSeconds: 30, HistoryRetentionDays: 30}, nil
		},
		func(context.Context, adminstore.TenantHealthSettings) error { return runErr },
		func(context.Context, int) error { return cleanupErr },
		func(err error) { reported <- err },
	)
	scheduler.Start(context.Background())
	select {
	case err := <-reported:
		if !errors.Is(err, runErr) { t.Fatalf("reported run error=%v", err) }
	case <-time.After(2 * time.Second):
		t.Fatal("scheduled run error was not reported")
	}
	select {
	case err := <-reported:
		if !errors.Is(err, cleanupErr) { t.Fatalf("reported cleanup error=%v", err) }
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup error was not reported")
	}
	scheduler.Close()
}
