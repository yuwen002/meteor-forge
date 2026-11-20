package adminapi

import (
	"context"
	"errors"
	"time"
)

type tenantHealthRetentionBatch func(context.Context, time.Time, int) (int64, error)
type tenantHealthRetentionRecorder func(context.Context, time.Time, time.Time, string, int64, int64, string) error

// executeTenantHealthRetentionCleanup runs both retention queues and persists
// the outcome, including counts from batches that committed before a failure.
// The recorder must enforce the caller's lease fencing.
func executeTenantHealthRetentionCleanup(
	ctx context.Context,
	retentionDays int,
	deleteHistory tenantHealthRetentionBatch,
	deleteRuns tenantHealthRetentionBatch,
	record tenantHealthRetentionRecorder,
) error {
	startedAt := time.Now().UTC()
	var deletedHistory, deletedRuns int64
	countHistory := func(batchCtx context.Context, before time.Time, limit int) (int64, error) {
		deleted, err := deleteHistory(batchCtx, before, limit)
		deletedHistory += deleted
		return deleted, err
	}
	countRuns := func(batchCtx context.Context, before time.Time, limit int) (int64, error) {
		deleted, err := deleteRuns(batchCtx, before, limit)
		deletedRuns += deleted
		return deleted, err
	}

	cleanupErr := cleanupTenantHealthHistory(ctx, countHistory, retentionDays)
	if cleanupErr == nil {
		cleanupErr = cleanupTenantHealthHistory(ctx, countRuns, retentionDays)
	}
	status, errorCode := "succeeded", ""
	if cleanupErr != nil {
		status, errorCode = "failed", "cleanup_failed"
	}

	recordCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	recordErr := record(recordCtx, startedAt, time.Now().UTC(), status, deletedHistory, deletedRuns, errorCode)
	if cleanupErr != nil && recordErr != nil {
		return errors.Join(cleanupErr, recordErr)
	}
	if cleanupErr != nil {
		return cleanupErr
	}
	return recordErr
}
