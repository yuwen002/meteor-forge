package adminapi

import (
	"context"
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
	closeOnce    sync.Once
}

func newTenantHealthScheduler(
	loadSettings func(context.Context) (adminstore.TenantHealthSettings, error),
	run func(context.Context, adminstore.TenantHealthSettings) error,
	cleanup func(context.Context, int) error,
) *tenantHealthScheduler {
	return &tenantHealthScheduler{
		loadSettings: loadSettings,
		run: run,
		cleanup: cleanup,
		notify: make(chan struct{}, 1),
		done: make(chan struct{}),
	}
}

// Start launches one scheduler loop. A disabled or unreadable setting never starts a check.
func (s *tenantHealthScheduler) Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	go s.loop(ctx)
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
	s.closeOnce.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
	})
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
		_ = s.run(roundCtx, settings)
		cancel()
		if ctx.Err() != nil {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(ctx, 5*time.Second)
		_ = s.cleanup(cleanupCtx, settings.HistoryRetentionDays)
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
