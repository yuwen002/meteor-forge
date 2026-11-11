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

type tenantHealthSchedulerLeaseContextKey struct{}

type tenantHealthSchedulerLeaseContext struct {
	ownerID string
	generation int64
}

var errTenantHealthSchedulerLeaseNotAcquired = errors.New("another API instance owns the tenant health scheduler lease")

func newTenantHealthSchedulerOwnerID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate tenant health scheduler owner ID: %w", err)
	}
	return hex.EncodeToString(value), nil
}

// runWithTenantHealthSchedulerLease coordinates schedulers sharing one metadata
// database. A heartbeat cancels the round if ownership is lost; expiry allows
// another process to take over after an abnormal exit.
func runWithTenantHealthSchedulerLease(ctx context.Context, store *adminstore.Store, ownerID string, run func(context.Context) error) error {
	if store == nil || ownerID == "" {
		return errors.New("tenant health scheduler lease is not configured")
	}
	now := time.Now().UTC()
	acquired, generation, err := store.AcquireTenantHealthSchedulerLease(ctx, ownerID, now, tenantHealthSchedulerLeaseTTL)
	if err != nil { return err }
	if !acquired { return errTenantHealthSchedulerLeaseNotAcquired }

	runCtx, cancel := context.WithCancel(ctx)
	heartbeatDone := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(tenantHealthSchedulerLeaseTTL / 3)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				heartbeatDone <- nil
				return
			case <-ticker.C:
				renewed, renewErr := store.RenewTenantHealthSchedulerLease(runCtx, ownerID, time.Now().UTC(), tenantHealthSchedulerLeaseTTL)
				if renewErr != nil {
					heartbeatDone <- fmt.Errorf("renew tenant health scheduler lease: %w", renewErr)
					cancel()
					return
				}
				if !renewed {
					heartbeatDone <- errors.New("tenant health scheduler lease ownership was lost")
					cancel()
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
	releaseErr := store.ReleaseTenantHealthSchedulerLease(releaseCtx, ownerID)
	releaseCancel()
	if heartbeatErr != nil { return heartbeatErr }
	if releaseErr != nil && ctx.Err() == nil { return releaseErr }
	return runErr
}
