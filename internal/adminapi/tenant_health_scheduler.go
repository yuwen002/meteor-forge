package adminapi

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/yuwen002/meteor-forge/internal/adminstore"
)

type tenantHealthSchedulerSnapshot struct {
	Enabled bool `json:"enabled"`
	Running bool `json:"running"`
	IntervalSeconds int `json:"interval_seconds"`
	TotalRuns uint64 `json:"total_runs"`
	SuccessCount uint64 `json:"success_count"`
	FailureCount uint64 `json:"failure_count"`
	LastStartedAt *time.Time `json:"last_started_at,omitempty"`
	LastFinishedAt *time.Time `json:"last_finished_at,omitempty"`
	LastSuccessAt *time.Time `json:"last_success_at,omitempty"`
	NextRunAt *time.Time `json:"next_run_at,omitempty"`
	LastDurationMS int64 `json:"last_duration_ms"`
	LastError string `json:"last_error,omitempty"`
	LeaseStatus string `json:"lease_status"`
	LeaseExpiresAt *time.Time `json:"lease_expires_at,omitempty"`
	LeaseGeneration int64 `json:"lease_generation"`
	LeaseRemainingSeconds int64 `json:"lease_remaining_seconds"`
}

type tenantHealthScheduler struct {
	loadSettings func(context.Context) (adminstore.TenantHealthSettings, error)
	run          func(context.Context, adminstore.TenantHealthSettings) error
	cleanup      func(context.Context, int) error
	notify       chan struct{}
	cancel       context.CancelFunc
	done         chan struct{}
	onError      func(error)
	lifecycleMu sync.Mutex
	started     bool
	closed      bool
	snapshotMu sync.RWMutex
	snapshot tenantHealthSchedulerSnapshot
}

func newTenantHealthScheduler(
	loadSettings func(context.Context) (adminstore.TenantHealthSettings, error),
	run func(context.Context, adminstore.TenantHealthSettings) error,
	cleanup func(context.Context, int) error,
	errorHandlers ...func(error),
) *tenantHealthScheduler {
	var onError func(error)
	if len(errorHandlers) > 0 {
		onError = errorHandlers[0]
	}
	return &tenantHealthScheduler{
		onError: onError,
		loadSettings: loadSettings,
		run: run,
		cleanup: cleanup,
		notify: make(chan struct{}, 1),
		done: make(chan struct{}),
	}
}

// Start launches one scheduler loop. A disabled or unreadable setting never starts a check.
func (s *tenantHealthScheduler) Start(parent context.Context) {
	s.lifecycleMu.Lock()
	if s.closed || s.started {
		s.lifecycleMu.Unlock()
		return
	}
	s.started = true
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.lifecycleMu.Unlock()
	go s.loop(ctx)
}

func (s *tenantHealthScheduler) Snapshot() tenantHealthSchedulerSnapshot {
	s.snapshotMu.RLock()
	defer s.snapshotMu.RUnlock()
	return s.snapshot
}

func (s *tenantHealthScheduler) updateSettings(settings adminstore.TenantHealthSettings) {
	s.snapshotMu.Lock()
	s.snapshot.Enabled = settings.Enabled
	s.snapshot.IntervalSeconds = settings.IntervalSeconds
	if !settings.Enabled { s.snapshot.NextRunAt = nil }
	s.snapshotMu.Unlock()
}

func (s *tenantHealthScheduler) setNextRun(at *time.Time) {
	s.snapshotMu.Lock()
	s.snapshot.NextRunAt = at
	s.snapshotMu.Unlock()
}

func (s *tenantHealthScheduler) beginRound() time.Time {
	now := time.Now().UTC()
	s.snapshotMu.Lock()
	s.snapshot.Running = true
	s.snapshot.NextRunAt = nil
	s.snapshot.TotalRuns++
	s.snapshot.LastStartedAt = &now
	s.snapshotMu.Unlock()
	return now
}

func (s *tenantHealthScheduler) finishRound(started time.Time, runErr error) {
	now := time.Now().UTC()
	s.snapshotMu.Lock()
	s.snapshot.Running = false
	s.snapshot.LastFinishedAt = &now
	s.snapshot.LastDurationMS = now.Sub(started).Milliseconds()
	if runErr == nil {
		s.snapshot.SuccessCount++
		s.snapshot.LastSuccessAt = &now
		s.snapshot.LastError = ""
	} else {
		s.snapshot.FailureCount++
		s.snapshot.LastError = "巡检执行失败"
	}
	s.snapshotMu.Unlock()
}

func (s *tenantHealthScheduler) setSnapshotError(message string) {
	s.snapshotMu.Lock()
	s.snapshot.LastError = message
	s.snapshotMu.Unlock()
}

func (s *tenantHealthScheduler) reportError(err error) {
	if err != nil && s.onError != nil {
		s.onError(err)
	}
}

// Refresh asks the loop to reload settings. Notifications are coalesced.
func (s *tenantHealthScheduler) Refresh() {
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

// Close cancels the current round and waits until the scheduler exits.
func (s *tenantHealthScheduler) Close() {
	s.lifecycleMu.Lock()
	if !s.closed {
		s.closed = true
		if s.started {
			if s.cancel != nil {
				s.cancel()
			}
		} else {
			close(s.done)
		}
	}
	s.lifecycleMu.Unlock()
	<-s.done
}

func (s *tenantHealthScheduler) loop(ctx context.Context) {
	defer close(s.done)
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		settings, err := s.loadSettings(ctx)
		if err != nil {
			s.setSnapshotError("读取巡检配置失败")
			s.reportError(fmt.Errorf("load tenant health settings: %w", err))
			if timer == nil { timer = time.NewTimer(30 * time.Second) } else { timer.Reset(30 * time.Second) }
			select { case <-ctx.Done(): return; case <-s.notify: continue; case <-timer.C: continue }
		}
		s.updateSettings(settings)
		if !settings.Enabled {
			select {
			case <-ctx.Done():
				return
			case <-s.notify:
				continue
			}
		}
		if timer == nil {
			timer = time.NewTimer(time.Duration(settings.IntervalSeconds) * time.Second)
		} else {
			timer.Reset(time.Duration(settings.IntervalSeconds) * time.Second)
		}
		nextRun := time.Now().UTC().Add(time.Duration(settings.IntervalSeconds) * time.Second)
		s.setNextRun(&nextRun)
		select {
		case <-ctx.Done():
			return
		case <-s.notify:
			s.setNextRun(nil)
			if !timer.Stop() {
				select { case <-timer.C: default: }
			}
			continue
		case <-timer.C:
			s.setNextRun(nil)
		}

		// Always re-read persisted settings after the timer fires. If Refresh and
		// the timer become ready together, select may choose the timer; the fresh
		// read prevents a disabled or updated configuration from running stale.
		dueSettings, enabled, settingsErr := s.settingsForDueRound(ctx)
		if settingsErr != nil {
			if ctx.Err() != nil { return }
			s.setSnapshotError("重新读取巡检配置失败")
			s.reportError(fmt.Errorf("reload tenant health settings before round: %w", settingsErr))
			continue
		}
		if !enabled { s.updateSettings(dueSettings); continue }
		settings = dueSettings
		s.updateSettings(settings)

		// The loop runs each round synchronously, so two rounds can never overlap.
		if ctx.Err() != nil {
			return
		}
		roundStarted := s.beginRound()
		roundCtx, cancel := context.WithCancel(ctx)
		runErr := s.run(roundCtx, settings)
		if errors.Is(runErr, errTenantHealthSchedulerLeaseNotAcquired) {
			cancel()
			s.snapshotMu.Lock()
			s.snapshot.Running = false
			s.snapshot.TotalRuns--
			s.snapshotMu.Unlock()
			continue
		}
		if runErr != nil && ctx.Err() == nil {
			s.reportError(fmt.Errorf("run scheduled tenant health checks: %w", runErr))
		}
		cancel()
		if ctx.Err() == nil { s.finishRound(roundStarted, runErr) } else {
			s.snapshotMu.Lock(); s.snapshot.Running = false; s.snapshotMu.Unlock()
		}
		if ctx.Err() != nil {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(ctx, 5*time.Second)
		if err := s.cleanup(cleanupCtx, settings.HistoryRetentionDays); err != nil && ctx.Err() == nil {
			s.setSnapshotError("清理巡检历史失败")
			s.reportError(fmt.Errorf("cleanup tenant health history: %w", err))
		}
		cleanupCancel()
	}
}


// settingsForDueRound re-reads persisted configuration after the timer fires.
// This closes the race where a timer and a Refresh notification become ready
// together and select chooses the timer even though the setting was disabled.
func (s *tenantHealthScheduler) settingsForDueRound(ctx context.Context) (adminstore.TenantHealthSettings, bool, error) {
	if err := ctx.Err(); err != nil {
		return adminstore.TenantHealthSettings{}, false, err
	}
	settings, err := s.loadSettings(ctx)
	if err != nil {
		return adminstore.TenantHealthSettings{}, false, err
	}
	if !settings.Enabled {
		return settings, false, nil
	}
	return settings, true, nil
}

// cleanupTenantHealthHistory drains expired rows in bounded batches. The caller
// supplies a short-lived context so a large backlog cannot block shutdown.
func cleanupTenantHealthHistory(
	ctx context.Context,
	deleteBatch func(context.Context, time.Time, int) (int64, error),
	retentionDays int,
) error {
	if retentionDays < 1 {
		return adminstore.ErrTenantHealthInvalidInput
	}
	before := time.Now().UTC().Add(-time.Duration(retentionDays) * 24 * time.Hour)
	const batchSize = 500
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		deleted, err := deleteBatch(ctx, before, batchSize)
		if err != nil {
			return err
		}
		if deleted < batchSize {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}
