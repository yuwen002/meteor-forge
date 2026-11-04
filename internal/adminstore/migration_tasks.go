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
}

// CreateMigrationTask persists a queued task before it is handed to a worker.
func (s *Store) CreateMigrationTask(ctx context.Context, task MigrationTask) (MigrationTask, error) {
	if task.ID == "" || task.DataSourceID == "" || task.Action == "" {
		return MigrationTask{}, fmt.Errorf("%w: task ID, data source ID and action are required", ErrInvalidInput)
	}
	now := time.Now().UTC()
	task.CreatedAt = now
	task.Status = "queued"
	_, err := s.db.ExecContext(ctx, `INSERT INTO admin_migration_tasks
		(id, data_source_id, action, target_version, steps, status, message, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		task.ID, task.DataSourceID, task.Action, task.TargetVersion, task.Steps,
		task.Status, "", now.Format(time.RFC3339Nano))
	if err != nil {
		return MigrationTask{}, fmt.Errorf("create migration task: %w", err)
	}
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
	result, err := s.db.ExecContext(ctx, `UPDATE admin_migration_tasks SET status = ?,
		message = ?, started_at = COALESCE(?, started_at), finished_at = ?
		WHERE id = ?`, status, message, started, finished, id)
	if err != nil { return fmt.Errorf("update migration task: %w", err) }
	count, err := result.RowsAffected()
	if err != nil { return err }
	if count == 0 { return ErrNotFound }
	return nil
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
