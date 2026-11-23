package adminapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/yuwen002/meteor-forge/internal/adminstore"
)

const tenantHealthSchedulerLeaseTTL = 30 * time.Second

var errTenantHealthSchedulerLeaseLost = errors.New("tenant health scheduler lease ownership was lost")

type tenantHealthSchedulerLeaseContextKey struct{}

type tenantHealthSchedulerLeaseContext struct {
	ownerID string
	generation int64
}

var errTenantHealthSchedulerLeaseNotAcquired = errors.New("another API instance owns the tenant health scheduler lease")
var errTenantHealthSchedulerLeaseAcquireFailed = errors.New("tenant health scheduler lease acquisition failed")

func newTenantHealthSchedulerOwnerID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate tenant health scheduler owner ID: %w", err)
	}
	return hex.EncodeToString(value), nil
}

// tenantHealthSchedulerLeaseHeartbeatError distinguishes a lost lease from a
// renewal interrupted by scheduler shutdown.
func tenantHealthSchedulerLeaseHeartbeatError(ctx context.Context, renewed bool, renewErr error) error {
	if ctx.Err() != nil {
		return nil
	}
	if renewErr != nil {
		return fmt.Errorf("%w: renewal failed: %w", errTenantHealthSchedulerLeaseLost, renewErr)
	}
	if !renewed {
		return errTenantHealthSchedulerLeaseLost
	}
	return nil
}

// runWithTenantHealthSchedulerLease coordinates schedulers sharing one metadata
// database. A heartbeat cancels the round if ownership is lost; expiry allows
// another process to take over after an abnormal exit.
func runWithTenantHealthSchedulerLease(ctx context.Context, store *adminstore.Store, ownerID string, run func(context.Context) error) error {
	return runWithTenantHealthSchedulerLeaseTTL(ctx, store, ownerID, tenantHealthSchedulerLeaseTTL, run)
}

// runWithTenantHealthSchedulerLeaseTTL keeps the production lease policy in the
// wrapper while allowing deterministic short-lease failure tests.
func runWithTenantHealthSchedulerLeaseTTL(ctx context.Context, store *adminstore.Store, ownerID string, ttl time.Duration, run func(context.Context) error) error {
	if store == nil || ownerID == "" || ttl < 5*time.Second || ttl > 5*time.Minute {
		return errors.New("tenant health scheduler lease is not configured")
	}
	now := time.Now().UTC()
	acquired, generation, err := store.AcquireTenantHealthSchedulerLease(ctx, ownerID, now, ttl)
	if err != nil {
		return fmt.Errorf("%w: %v", errTenantHealthSchedulerLeaseAcquireFailed, err)
	}
	if !acquired {
		return errTenantHealthSchedulerLeaseNotAcquired
	}

	runCtx, cancel := context.WithCancel(ctx)
	heartbeatDone := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(ttl / 3)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				heartbeatDone <- nil
				return
			case <-ticker.C:
				renewed, renewErr := store.RenewTenantHealthSchedulerLeaseWithGeneration(runCtx, ownerID, generation, time.Now().UTC(), ttl)
				if heartbeatErr := tenantHealthSchedulerLeaseHeartbeatError(runCtx, renewed, renewErr); heartbeatErr != nil {
					// Prefer an already-requested shutdown over a concurrent renewal
					// failure; cancellation is not evidence that ownership was lost.
					select {
					case <-runCtx.Done():
						heartbeatDone <- nil
					default:
						heartbeatDone <- heartbeatErr
						cancel()
					}
					return
				}
				if runCtx.Err() != nil {
					heartbeatDone <- nil
					return
				}
			}
		}
	}()

	leaseCtx := context.WithValue(runCtx, tenantHealthSchedulerLeaseContextKey{}, tenantHealthSchedulerLeaseContext{ownerID: ownerID, generation: generation})
	runErr := run(leaseCtx)
	cancel()
	heartbeatErr := <-heartbeatDone
	releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 3*time.Second)
	releaseErr := store.ReleaseTenantHealthSchedulerLeaseWithGeneration(releaseCtx, ownerID, generation)
	releaseCancel()
	if heartbeatErr != nil {
		return heartbeatErr
	}
	if releaseErr != nil && ctx.Err() == nil {
		return releaseErr
	}
	// A protected write may detect takeover before the next heartbeat fires.
	// Preserve the scheduler's lease-lost classification in that case too.
	if errors.Is(runErr, adminstore.ErrTenantHealthSchedulerLeaseLost) {
		return errors.Join(errTenantHealthSchedulerLeaseLost, runErr)
	}
	return runErr
}
