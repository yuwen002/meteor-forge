package adminstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrMigrationTaskNotInterrupted = errors.New("migration task is not interrupted")
	ErrMigrationTaskDispositionExists = errors.New("migration task already has a manual disposition")
)

// MigrationTaskDisposition is an immutable administrator decision record.
// It never changes a task state and never schedules or executes SQL.
type MigrationTaskDisposition struct {
	ID int64 `json:"id"`
	TaskID string `json:"task_id"`
	DataSourceID string `json:"data_source_id"`
	Action string `json:"action"`
	Reconciliation json.RawMessage `json:"reconciliation"`
	OperatorNote string `json:"operator_note"`
	Actor string `json:"actor"`
	RequestSource string `json:"request_source,omitempty"`
	OccurredAt time.Time `json:"occurred_at"`
}

// migrateMigrationTaskDispositions adds the immutable manual-decision ledger.
func migrateMigrationTaskDispositions(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS admin_migration_task_dispositions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		task_id TEXT NOT NULL UNIQUE,
		data_source_id TEXT NOT NULL,
		action TEXT NOT NULL CHECK(action IN ('confirm_completed','authorize_retry','terminate')),
		reconciliation_json TEXT NOT NULL,
		operator_note TEXT NOT NULL,
		actor TEXT NOT NULL,
		request_source TEXT NOT NULL DEFAULT '',
		occurred_at TEXT NOT NULL,
		FOREIGN KEY(task_id) REFERENCES admin_migration_tasks(id) ON DELETE RESTRICT
	)`)
	if err != nil {
		return fmt.Errorf("create migration task disposition ledger: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_admin_migration_task_dispositions_time
		ON admin_migration_task_dispositions(occurred_at DESC, id DESC)`); err != nil {
		return fmt.Errorf("index migration task disposition ledger: %w", err)
	}
	return nil
}

// CreateMigrationTaskDisposition records exactly one manual decision for an
// interrupted task. The task row is write-locked before validation so parallel
// requests cannot both pass the state check. The original task is never changed.
func (s *Store) CreateMigrationTaskDisposition(ctx context.Context, item MigrationTaskDisposition) (MigrationTaskDisposition, error) {
	item.TaskID = strings.TrimSpace(item.TaskID)
	item.Actor = strings.TrimSpace(item.Actor)
	item.RequestSource = strings.TrimSpace(item.RequestSource)
	item.OperatorNote = strings.TrimSpace(item.OperatorNote)
	switch item.Action {
	case "confirm_completed", "authorize_retry", "terminate":
	default:
		return MigrationTaskDisposition{}, fmt.Errorf("%w: unsupported migration task disposition", ErrInvalidInput)
	}
	if item.TaskID == "" || item.Actor == "" || len(item.Actor) > 128 ||
		len(item.RequestSource) > 128 || item.OperatorNote == "" || len(item.OperatorNote) > 2000 {
		return MigrationTaskDisposition{}, fmt.Errorf("%w: invalid migration task disposition fields", ErrInvalidInput)
	}
	if len(item.Reconciliation) == 0 || len(item.Reconciliation) > 16<<10 || !json.Valid(item.Reconciliation) {
		return MigrationTaskDisposition{}, fmt.Errorf("%w: reconciliation snapshot must be valid JSON up to 16 KiB", ErrInvalidInput)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MigrationTaskDisposition{}, fmt.Errorf("begin migration task disposition: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	lockResult, err := tx.ExecContext(ctx, "UPDATE admin_migration_tasks SET status = status WHERE id = ?", item.TaskID)
	if err != nil {
		return MigrationTaskDisposition{}, fmt.Errorf("lock migration task for manual disposition: %w", err)
	}
	locked, err := lockResult.RowsAffected()
	if err != nil {
		return MigrationTaskDisposition{}, fmt.Errorf("check migration task lock: %w", err)
	}
	if locked == 0 {
		return MigrationTaskDisposition{}, ErrNotFound
	}
	var status string
	if err := tx.QueryRowContext(ctx, "SELECT data_source_id, status FROM admin_migration_tasks WHERE id = ?", item.TaskID).
		Scan(&item.DataSourceID, &status); err != nil {
		return MigrationTaskDisposition{}, fmt.Errorf("read migration task for manual disposition: %w", err)
	}
	if status != "interrupted" {
		return MigrationTaskDisposition{}, ErrMigrationTaskNotInterrupted
	}
	item.OccurredAt = time.Now().UTC()
	result, err := tx.ExecContext(ctx, `INSERT INTO admin_migration_task_dispositions
		(task_id, data_source_id, action, reconciliation_json, operator_note, actor, request_source, occurred_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		item.TaskID, item.DataSourceID, item.Action, string(item.Reconciliation),
		item.OperatorNote, item.Actor, item.RequestSource, item.OccurredAt.Format(time.RFC3339Nano))
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return MigrationTaskDisposition{}, ErrMigrationTaskDispositionExists
		}
		return MigrationTaskDisposition{}, fmt.Errorf("insert migration task disposition: %w", err)
	}
	item.ID, err = result.LastInsertId()
	if err != nil {
		return MigrationTaskDisposition{}, fmt.Errorf("read migration task disposition ID: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return MigrationTaskDisposition{}, fmt.Errorf("commit migration task disposition: %w", err)
	}
	return item, nil
}

// GetMigrationTaskDisposition returns the immutable decision record for a task.
func (s *Store) GetMigrationTaskDisposition(ctx context.Context, taskID string) (MigrationTaskDisposition, error) {
	var item MigrationTaskDisposition
	var snapshot, occurredAt string
	err := s.db.QueryRowContext(ctx, `SELECT id, task_id, data_source_id, action,
		reconciliation_json, operator_note, actor, request_source, occurred_at
		FROM admin_migration_task_dispositions WHERE task_id = ?`, taskID).Scan(
		&item.ID, &item.TaskID, &item.DataSourceID, &item.Action, &snapshot,
		&item.OperatorNote, &item.Actor, &item.RequestSource, &occurredAt)
	if errors.Is(err, sql.ErrNoRows) {
		return MigrationTaskDisposition{}, ErrNotFound
	}
	if err != nil {
		return MigrationTaskDisposition{}, fmt.Errorf("get migration task disposition: %w", err)
	}
	item.Reconciliation = json.RawMessage(snapshot)
	item.OccurredAt, err = time.Parse(time.RFC3339Nano, occurredAt)
	if err != nil {
		return MigrationTaskDisposition{}, fmt.Errorf("parse migration task disposition timestamp: %w", err)
	}
	return item, nil
}
