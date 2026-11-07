package adminapi

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/yuwen002/meteor-forge/internal/adminstore"
)

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
			s.reportError(fmt.Errorf("load tenant health settings: %w", err))
			if timer == nil { timer = time.NewTimer(30 * time.Second) } else { timer.Reset(30 * time.Second) }
			select { case <-ctx.Done(): return; case <-s.notify: continue; case <-timer.C: continue }
		}
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
		select {
		case <-ctx.Done():
			return
		case <-s.notify:
			if !timer.Stop() {
				select { case <-timer.C: default: }
			}
			continue
		case <-timer.C:
		}

		// The loop runs each round synchronously, so two rounds can never overlap.
		if ctx.Err() != nil {
			return
		}
		roundCtx, cancel := context.WithCancel(ctx)
		if err := s.run(roundCtx, settings); err != nil && ctx.Err() == nil {
			s.reportError(fmt.Errorf("run scheduled tenant health checks: %w", err))
		}
		cancel()
		if ctx.Err() != nil {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(ctx, 5*time.Second)
		if err := s.cleanup(cleanupCtx, settings.HistoryRetentionDays); err != nil && ctx.Err() == nil {
			s.reportError(fmt.Errorf("cleanup tenant health history: %w", err))
		}
		cleanupCancel()
	}
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
