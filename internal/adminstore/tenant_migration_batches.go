package adminstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var (
	ErrTenantMigrationBatchNotFound = errors.New("tenant migration batch not found")
	ErrTenantMigrationBatchTransition = errors.New("invalid tenant migration batch transition")
)

// TenantMigrationBatchFailure describes a tenant whose child task could not be submitted.
type TenantMigrationBatchFailure struct {
	TenantID string `json:"tenant_id"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

// TenantMigrationBatchRecord persists the batch submission result independently of child tasks.
type TenantMigrationBatchRecord struct {
	ID                 string
	Total              int
	Queued             int
	SubmissionFailed   int
	SubmissionFailures []TenantMigrationBatchFailure
	Status             string
	Actor              string
	RequestSource      string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// TenantMigrationBatchAuditEvent records the batch-level submission lifecycle.
type TenantMigrationBatchAuditEvent struct {
	ID            int64     `json:"id"`
	BatchID       string    `json:"batch_id"`
	Event         string    `json:"event"`
	Actor         string    `json:"actor"`
	RequestSource string    `json:"request_source,omitempty"`
	Message       string    `json:"message,omitempty"`
	OccurredAt    time.Time `json:"occurred_at"`
}

// CreateTenantMigrationBatch persists the batch before child tasks are submitted.
func (s *Store) CreateTenantMigrationBatch(ctx context.Context, batch TenantMigrationBatchRecord) error {
	if batch.ID == "" || batch.Total < 1 || batch.Total > 100 {
		return fmt.Errorf("%w: invalid tenant migration batch", ErrInvalidInput)
	}
	now := time.Now().UTC()
	stamp := now.Format(time.RFC3339Nano)
	actor := batch.Actor
	if actor == "" {
		actor = "local-api"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tenant migration batch: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	_, err = tx.ExecContext(ctx, `INSERT INTO admin_tenant_migration_batches
		(id, total, queued, submission_failed, submission_failures, status, actor, request_source, created_at, updated_at)
		VALUES (?, ?, 0, 0, '[]', 'submitting', ?, ?, ?, ?)`,
		batch.ID, batch.Total, actor, batch.RequestSource, stamp, stamp)
	if err != nil {
		return fmt.Errorf("create tenant migration batch: %w", err)
	}
	if err := insertTenantMigrationBatchAudit(ctx, tx, batch.ID, actor, batch.RequestSource,
		"submission_started", "tenant migration batch submission started", now); err != nil {
		return err
	}
	return tx.Commit()
}

// CompleteTenantMigrationBatch stores every child submission outcome and its audit event atomically.
func (s *Store) CompleteTenantMigrationBatch(ctx context.Context, id string, queued int, failures []TenantMigrationBatchFailure) error {
	if id == "" || queued < 0 || queued > 100 {
		return fmt.Errorf("%w: invalid tenant migration batch result", ErrInvalidInput)
	}
	encoded, err := json.Marshal(failures)
	if err != nil {
		return fmt.Errorf("encode tenant migration batch failures: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tenant migration batch completion: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	var total int
	var actor, requestSource string
	if err := tx.QueryRowContext(ctx, `SELECT total, actor, request_source
		FROM admin_tenant_migration_batches WHERE id = ?`, id).Scan(&total, &actor, &requestSource); errors.Is(err, sql.ErrNoRows) {
		return ErrTenantMigrationBatchNotFound
	} else if err != nil {
		return fmt.Errorf("load tenant migration batch: %w", err)
	}
	if queued+len(failures) != total {
		return fmt.Errorf("%w: batch result count does not match total", ErrInvalidInput)
	}
	status := "submitted"
	if len(failures) > 0 {
		status = "partially_submitted"
		if queued == 0 {
			status = "submission_failed"
		}
	}
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `UPDATE admin_tenant_migration_batches
		SET queued = ?, submission_failed = ?, submission_failures = ?, status = ?, updated_at = ?
		WHERE id = ? AND status = 'submitting'`,
		queued, len(failures), string(encoded), status, now.Format(time.RFC3339Nano), id)
	if err != nil {
		return fmt.Errorf("complete tenant migration batch: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count tenant migration batch update: %w", err)
	}
	if affected != 1 {
		return ErrTenantMigrationBatchTransition
	}
	message := fmt.Sprintf("queued %d of %d tenant migration tasks; %d submission failures", queued, total, len(failures))
	if err := insertTenantMigrationBatchAudit(ctx, tx, id, actor, requestSource, "submission_completed", message, now); err != nil {
		return err
	}
	return tx.Commit()
}

// GetTenantMigrationBatch returns the durable submission record, including failures
// that occurred before a child migration task could be created.
func (s *Store) GetTenantMigrationBatch(ctx context.Context, id string) (TenantMigrationBatchRecord, error) {
	var batch TenantMigrationBatchRecord
	var failuresJSON, created, updated string
	err := s.db.QueryRowContext(ctx, `SELECT id, total, queued, submission_failed, submission_failures,
		status, actor, request_source, created_at, updated_at
		FROM admin_tenant_migration_batches WHERE id = ?`, id).Scan(
		&batch.ID, &batch.Total, &batch.Queued, &batch.SubmissionFailed, &failuresJSON,
		&batch.Status, &batch.Actor, &batch.RequestSource, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return TenantMigrationBatchRecord{}, ErrTenantMigrationBatchNotFound
	}
	if err != nil {
		return TenantMigrationBatchRecord{}, fmt.Errorf("get tenant migration batch: %w", err)
	}
	if err := json.Unmarshal([]byte(failuresJSON), &batch.SubmissionFailures); err != nil {
		return TenantMigrationBatchRecord{}, fmt.Errorf("decode tenant migration batch failures: %w", err)
	}
	batch.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return TenantMigrationBatchRecord{}, fmt.Errorf("parse tenant migration batch creation time: %w", err)
	}
	batch.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return TenantMigrationBatchRecord{}, fmt.Errorf("parse tenant migration batch update time: %w", err)
	}
	return batch, nil
}

// ListTenantMigrationBatchAuditEvents returns the batch-level submission audit trail.
func (s *Store) ListTenantMigrationBatchAuditEvents(ctx context.Context, id string) ([]TenantMigrationBatchAuditEvent, error) {
	if _, err := s.GetTenantMigrationBatch(ctx, id); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, batch_id, event, actor, request_source, message, occurred_at
		FROM admin_tenant_migration_batch_audit_events WHERE batch_id = ? ORDER BY id ASC`, id)
	if err != nil {
		return nil, fmt.Errorf("list tenant migration batch audit events: %w", err)
	}
	defer rows.Close()
	items := make([]TenantMigrationBatchAuditEvent, 0, 2)
	for rows.Next() {
		var item TenantMigrationBatchAuditEvent
		var occurred string
		if err := rows.Scan(&item.ID, &item.BatchID, &item.Event, &item.Actor, &item.RequestSource, &item.Message, &occurred); err != nil {
			return nil, fmt.Errorf("scan tenant migration batch audit event: %w", err)
		}
		item.OccurredAt, err = time.Parse(time.RFC3339Nano, occurred)
		if err != nil {
			return nil, fmt.Errorf("parse tenant migration batch audit timestamp: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read tenant migration batch audit events: %w", err)
	}
	return items, nil
}

func insertTenantMigrationBatchAudit(ctx context.Context, tx *sql.Tx, batchID, actor, requestSource, event, message string, occurred time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO admin_tenant_migration_batch_audit_events
		(batch_id, event, actor, request_source, message, occurred_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		batchID, event, actor, requestSource, message, occurred.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("record tenant migration batch audit event: %w", err)
	}
	return nil
}
