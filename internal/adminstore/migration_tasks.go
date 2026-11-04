package adminstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// MigrationTask is a durable record of a migration operation. It deliberately
// contains no DSN, SQL text, or raw driver error.
var ErrInvalidTaskTransition = errors.New("invalid migration task state transition")

type MigrationTask struct {
	ID           string     `json:"id"`
	DataSourceID string     `json:"data_source_id"`
	Action       string     `json:"action"`
	TargetVersion string    `json:"target_version,omitempty"`
	Steps        int        `json:"steps,omitempty"`
	Status       string     `json:"status"`
	Message      string     `json:"message,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	RequestSource string    `json:"-"`
}

type MigrationAuditEvent struct {
	ID int64 `json:"id"`
	TaskID string `json:"task_id"`
	DataSourceID string `json:"data_source_id"`
	Action string `json:"action"`
	FromStatus string `json:"from_status,omitempty"`
	ToStatus string `json:"to_status"`
	Actor string `json:"actor"`
	RequestSource string `json:"request_source,omitempty"`
	Message string `json:"message,omitempty"`
	OccurredAt time.Time `json:"occurred_at"`
}

// CreateMigrationTask persists a queued task before it is handed to a worker.
func (s *Store) CreateMigrationTask(ctx context.Context, task MigrationTask) (MigrationTask, error) {
	if task.ID == "" || task.DataSourceID == "" || task.Action == "" {
		return MigrationTask{}, fmt.Errorf("%w: task ID, data source ID and action are required", ErrInvalidInput)
	}
	now := time.Now().UTC()
	task.CreatedAt = now
	task.Status = "queued"
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil { return MigrationTask{}, fmt.Errorf("begin migration task creation: %w", err) }
	defer tx.Rollback() //nolint:errcheck
	_, err = tx.ExecContext(ctx, `INSERT INTO admin_migration_tasks
		(id, data_source_id, action, target_version, steps, status, message, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		task.ID, task.DataSourceID, task.Action, task.TargetVersion, task.Steps,
		task.Status, "", now.Format(time.RFC3339Nano))
	if err != nil { return MigrationTask{}, fmt.Errorf("create migration task: %w", err) }
	if err := insertMigrationAuditEvent(ctx, tx, task, "", "queued", "task submitted", now); err != nil {
		return MigrationTask{}, err
	}
	if err := tx.Commit(); err != nil { return MigrationTask{}, fmt.Errorf("commit migration task: %w", err) }
	return task, nil
}

// GetMigrationTask returns one task record.
func (s *Store) GetMigrationTask(ctx context.Context, id string) (MigrationTask, error) {
	var task MigrationTask
	var created string
	var started, finished sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id, data_source_id, action, target_version,
		steps, status, message, created_at, started_at, finished_at
		FROM admin_migration_tasks WHERE id = ?`, id).Scan(
		&task.ID, &task.DataSourceID, &task.Action, &task.TargetVersion,
		&task.Steps, &task.Status, &task.Message, &created, &started, &finished)
	if errors.Is(err, sql.ErrNoRows) { return MigrationTask{}, ErrNotFound }
	if err != nil { return MigrationTask{}, fmt.Errorf("get migration task: %w", err) }
	task.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil { return MigrationTask{}, fmt.Errorf("parse task creation time: %w", err) }
	if started.Valid {
		value, parseErr := time.Parse(time.RFC3339Nano, started.String)
		if parseErr != nil { return MigrationTask{}, fmt.Errorf("parse task start time: %w", parseErr) }
		task.StartedAt = &value
	}
	if finished.Valid {
		value, parseErr := time.Parse(time.RFC3339Nano, finished.String)
		if parseErr != nil { return MigrationTask{}, fmt.Errorf("parse task finish time: %w", parseErr) }
		task.FinishedAt = &value
	}
	return task, nil
}

// ListMigrationTasks returns the newest task records first.
func (s *Store) ListMigrationTasks(ctx context.Context, limit int) ([]MigrationTask, error) {
	if limit <= 0 || limit > 200 { limit = 50 }
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM admin_migration_tasks
		ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil { return nil, fmt.Errorf("list migration tasks: %w", err) }
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil { return nil, err }
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil { return nil, err }
	items := make([]MigrationTask, 0, len(ids))
	for _, id := range ids {
		item, err := s.GetMigrationTask(ctx, id)
		if err != nil { return nil, err }
		items = append(items, item)
	}
	return items, nil
}

// UpdateMigrationTask performs a constrained state transition and stores safe metadata.
func (s *Store) UpdateMigrationTask(ctx context.Context, id, status, message string, startedAt, finishedAt *time.Time) error {
	switch status {
	case "running", "succeeded", "failed", "canceled", "interrupted":
	default:
		return fmt.Errorf("%w: invalid migration task status", ErrInvalidInput)
	}
	var started, finished any
	if startedAt != nil { started = startedAt.UTC().Format(time.RFC3339Nano) }
	if finishedAt != nil { finished = finishedAt.UTC().Format(time.RFC3339Nano) }
	allowedFrom := ""
	switch status {
	case "running": allowedFrom = "queued"
	case "succeeded": allowedFrom = "running"
	case "failed", "canceled", "interrupted": allowedFrom = "queued', 'running"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil { return fmt.Errorf("begin migration task transition: %w", err) }
	defer tx.Rollback() //nolint:errcheck
	var task MigrationTask
	if err := tx.QueryRowContext(ctx, `SELECT id, data_source_id, action, status, message FROM admin_migration_tasks WHERE id = ?`, id).
		Scan(&task.ID, &task.DataSourceID, &task.Action, &task.Status, &task.Message); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil { return fmt.Errorf("load migration task for transition: %w", err) }
	from := task.Status
	_ = tx.QueryRowContext(ctx, `SELECT request_source FROM admin_migration_audit_events
		WHERE task_id = ? ORDER BY id ASC LIMIT 1`, id).Scan(&task.RequestSource)
	query := `UPDATE admin_migration_tasks SET status = ?, message = ?,
		started_at = COALESCE(?, started_at), finished_at = ?
		WHERE id = ? AND status IN ('` + allowedFrom + `')`
	result, err := tx.ExecContext(ctx, query, status, message, started, finished, id)
	if err != nil { return fmt.Errorf("update migration task: %w", err) }
	count, err := result.RowsAffected()
	if err != nil { return err }
	if count == 0 { return ErrInvalidTaskTransition }
	now := time.Now().UTC()
	if finishedAt != nil { now = finishedAt.UTC() } else if startedAt != nil { now = startedAt.UTC() }
	task.RequestSource = ""
	if err := insertMigrationAuditEvent(ctx, tx, task, from, status, message, now); err != nil { return err }
	return tx.Commit()
}

func insertMigrationAuditEvent(ctx context.Context, tx *sql.Tx, task MigrationTask, from, to, message string, at time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO admin_migration_audit_events
		(task_id, data_source_id, action, from_status, to_status, actor, request_source, message, occurred_at)
		VALUES (?, ?, ?, ?, ?, 'local-api', ?, ?, ?)`,
		task.ID, task.DataSourceID, task.Action, from, to, task.RequestSource, message, at.UTC().Format(time.RFC3339Nano))
	if err != nil { return fmt.Errorf("record migration audit event: %w", err) }
	return nil
}

// ListMigrationAuditEvents returns chronological state changes for one task.
func (s *Store) ListMigrationAuditEvents(ctx context.Context, taskID string) ([]MigrationAuditEvent, error) {
	if _, err := s.GetMigrationTask(ctx, taskID); err != nil { return nil, err }
	rows, err := s.db.QueryContext(ctx, `SELECT id, task_id, data_source_id, action, from_status, to_status,
		actor, request_source, message, occurred_at FROM admin_migration_audit_events
		WHERE task_id = ? ORDER BY id ASC`, taskID)
	if err != nil { return nil, fmt.Errorf("list migration audit events: %w", err) }
	defer rows.Close()
	items := make([]MigrationAuditEvent, 0)
	for rows.Next() {
		var item MigrationAuditEvent
		var at string
		if err := rows.Scan(&item.ID, &item.TaskID, &item.DataSourceID, &item.Action, &item.FromStatus,
			&item.ToStatus, &item.Actor, &item.RequestSource, &item.Message, &at); err != nil { return nil, err }
		item.OccurredAt, err = time.Parse(time.RFC3339Nano, at)
		if err != nil { return nil, fmt.Errorf("parse migration audit timestamp: %w", err) }
		items = append(items, item)
	}
	return items, rows.Err()
}

// RecoverInterruptedTasks marks tasks left running or queued by a process crash.
func (s *Store) RecoverInterruptedTasks(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE admin_migration_tasks
		SET status = 'interrupted', message = 'service restarted before task completion',
		finished_at = ? WHERE status IN ('queued', 'running')`,
		time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil { return fmt.Errorf("recover interrupted migration tasks: %w", err) }
	return nil
}
