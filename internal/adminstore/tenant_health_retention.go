package adminstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type TenantHealthRetentionCleanup struct {
	Status string `json:"status"`
	StartedAt *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	DeletedHistoryCount int64 `json:"deleted_history_count"`
	DeletedRunCount int64 `json:"deleted_run_count"`
	ErrorCode string `json:"error_code,omitempty"`
}

// RecordTenantHealthRetentionCleanupWithLease updates the durable cleanup
// summary only while the caller still owns the current unexpired fencing lease.
func (s *Store) RecordTenantHealthRetentionCleanupWithLease(
	ctx context.Context, ownerID string, generation int64, startedAt, finishedAt time.Time,
	status string, deletedHistory, deletedRuns int64, errorCode string,
) error {
	if strings.TrimSpace(ownerID) == "" || len(ownerID) > 128 || generation <= 0 ||
		startedAt.IsZero() || finishedAt.IsZero() || finishedAt.Before(startedAt) || deletedHistory < 0 || deletedRuns < 0 {
		return ErrTenantHealthSchedulerLeaseInvalid
	}
	if status != "succeeded" && status != "failed" {
		return ErrTenantHealthInvalidInput
	}
	if status == "succeeded" {
		errorCode = ""
	} else if errorCode != "cleanup_failed" {
		errorCode = "cleanup_failed"
	}
	now := finishedAt.UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE admin_tenant_health_retention_cleanup
		SET started_at = ?, finished_at = ?, status = ?,
			deleted_history_count = ?, deleted_run_count = ?, error_code = ?
		WHERE id = 1 AND EXISTS (
			SELECT 1 FROM admin_tenant_health_scheduler_lease
			WHERE id = 1 AND owner_id = ? AND generation = ? AND lease_until > ?
		)`,
		formatTenantHealthTime(startedAt.UTC()), formatTenantHealthTime(now), status,
		deletedHistory, deletedRuns, errorCode, ownerID, generation, now.UnixNano())
	if err != nil {
		return fmt.Errorf("record tenant health retention cleanup: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read tenant health retention cleanup update count: %w", err)
	}
	if affected != 1 {
		return ErrTenantHealthSchedulerLeaseLost
	}
	return nil
}

func (s *Store) GetTenantHealthRetentionCleanup(ctx context.Context) (TenantHealthRetentionCleanup, error) {
	var item TenantHealthRetentionCleanup
	var startedAt, finishedAt sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT status, started_at, finished_at,
		deleted_history_count, deleted_run_count, error_code
		FROM admin_tenant_health_retention_cleanup WHERE id = 1`).Scan(
		&item.Status, &startedAt, &finishedAt, &item.DeletedHistoryCount,
		&item.DeletedRunCount, &item.ErrorCode)
	if errors.Is(err, sql.ErrNoRows) {
		return TenantHealthRetentionCleanup{}, fmt.Errorf("tenant health retention cleanup state is not initialized")
	}
	if err != nil {
		return TenantHealthRetentionCleanup{}, fmt.Errorf("read tenant health retention cleanup: %w", err)
	}
	if startedAt.Valid {
		value, parseErr := time.Parse(time.RFC3339Nano, startedAt.String)
		if parseErr != nil { return TenantHealthRetentionCleanup{}, fmt.Errorf("parse retention cleanup start time: %w", parseErr) }
		item.StartedAt = &value
	}
	if finishedAt.Valid {
		value, parseErr := time.Parse(time.RFC3339Nano, finishedAt.String)
		if parseErr != nil { return TenantHealthRetentionCleanup{}, fmt.Errorf("parse retention cleanup finish time: %w", parseErr) }
		item.FinishedAt = &value
	}
	return item, nil
}
