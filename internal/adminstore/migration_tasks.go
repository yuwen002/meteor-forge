package adminstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// MigrationTask is a durable record of a migration operation. It deliberately
// contains no DSN, SQL text, or raw driver error.
var ErrInvalidTaskTransition = errors.New("invalid migration task state transition")

type MigrationTask struct {
	ID           string     `json:"id"`
	BatchID      string     `json:"batch_id,omitempty"`
	TenantID     string     `json:"tenant_id,omitempty"`
	DataSourceID string     `json:"data_source_id"`
	Action       string     `json:"action"`
	TargetVersion string    `json:"target_version,omitempty"`
	Steps        int        `json:"steps,omitempty"`
	Status       string     `json:"status"`
	Message      string     `json:"message,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	CurrentVersion string   `json:"current_version,omitempty"`
	CompletedSteps int      `json:"completed_steps"`
	TotalSteps int           `json:"total_steps"`
	CurrentAction string     `json:"current_action,omitempty"`
	ProgressUpdatedAt *time.Time `json:"progress_updated_at,omitempty"`
	RequestSource string    `json:"-"`
	Actor         string    `json:"-"`
}

type MigrationTaskProgress struct {
	CurrentVersion string
	CompletedSteps int
	TotalSteps int
	CurrentAction string
}

type MigrationAuditEvent struct {
	ID int64 `json:"id"`
	TaskID string `json:"task_id"`
	TenantID string `json:"tenant_id,omitempty"`
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
		(id, data_source_id, action, target_version, steps, status, message, created_at, batch_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		task.ID, task.DataSourceID, task.Action, task.TargetVersion, task.Steps,
		task.Status, "", now.Format(time.RFC3339Nano), task.BatchID)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), strings.ToLower(ErrActiveShardDDLTasks.Error())) { return MigrationTask{}, ErrActiveShardDDLTasks }
		return MigrationTask{}, fmt.Errorf("create migration task: %w", err)
	}
	if task.TenantID != "" {
		var mappedDataSourceID, tenantStatus string
		if err := tx.QueryRowContext(ctx, `SELECT data_source_id, status FROM admin_tenants WHERE id = ?`, task.TenantID).Scan(&mappedDataSourceID, &tenantStatus); err != nil {
			if errors.Is(err, sql.ErrNoRows) { return MigrationTask{}, ErrTenantNotFound }
			return MigrationTask{}, fmt.Errorf("verify migration tenant: %w", err)
		}
		if mappedDataSourceID != task.DataSourceID || tenantStatus != "active" {
			return MigrationTask{}, ErrTenantMigrationUnavailable
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO admin_migration_task_tenants(task_id, tenant_id) VALUES (?, ?)`, task.ID, task.TenantID); err != nil {
			return MigrationTask{}, fmt.Errorf("associate migration task with tenant: %w", err)
		}
	}
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
	var progressUpdated sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT t.id, t.data_source_id, t.action, t.target_version,
		t.steps, t.status, t.message, t.created_at, t.started_at, t.finished_at,
		COALESCE(p.current_version, ''), COALESCE(p.completed_steps, 0),
		COALESCE(p.total_steps, 0), COALESCE(p.current_action, ''), p.updated_at,
		COALESCE(tt.tenant_id, ''), t.batch_id
		FROM admin_migration_tasks t
		LEFT JOIN admin_migration_task_progress p ON p.task_id = t.id
		LEFT JOIN admin_migration_task_tenants tt ON tt.task_id = t.id
		WHERE t.id = ?`, id).Scan(
		&task.ID, &task.DataSourceID, &task.Action, &task.TargetVersion,
		&task.Steps, &task.Status, &task.Message, &created, &started, &finished,
		&task.CurrentVersion, &task.CompletedSteps, &task.TotalSteps, &task.CurrentAction, &progressUpdated, &task.TenantID, &task.BatchID)
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
	if progressUpdated.Valid {
		value, parseErr := time.Parse(time.RFC3339Nano, progressUpdated.String)
		if parseErr != nil { return MigrationTask{}, fmt.Errorf("parse task progress timestamp: %w", parseErr) }
		task.ProgressUpdatedAt = &value
	}
	return task, nil
}

// UpdateMigrationTaskProgress stores a bounded, safe progress snapshot for a running task.
func (s *Store) UpdateMigrationTaskProgress(ctx context.Context, id string, progress MigrationTaskProgress) error {
	if progress.CompletedSteps < 0 || progress.TotalSteps < 0 || progress.CompletedSteps > progress.TotalSteps {
		return fmt.Errorf("%w: invalid migration task progress", ErrInvalidInput)
	}
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `INSERT INTO admin_migration_task_progress
		(task_id, current_version, completed_steps, total_steps, current_action, updated_at)
		SELECT id, ?, ?, ?, ?, ? FROM admin_migration_tasks WHERE id = ? AND status = 'running'
		ON CONFLICT(task_id) DO UPDATE SET current_version = excluded.current_version,
		completed_steps = excluded.completed_steps, total_steps = excluded.total_steps,
		current_action = excluded.current_action, updated_at = excluded.updated_at
		WHERE EXISTS (SELECT 1 FROM admin_migration_tasks WHERE id = ? AND status = 'running')`,
		progress.CurrentVersion, progress.CompletedSteps, progress.TotalSteps, progress.CurrentAction,
		now.Format(time.RFC3339Nano), id, id)
	if err != nil { return fmt.Errorf("update migration task progress: %w", err) }
	affected, err := result.RowsAffected()
	if err != nil { return err }
	if affected == 0 {
		if _, err := s.GetMigrationTask(ctx, id); err != nil { return err }
		return ErrInvalidTaskTransition
	}
	return nil
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


 // ListMigrationTasksByBatch returns all durable child tasks for one batch.
func (s *Store) ListMigrationTasksByBatch(ctx context.Context, batchID string) ([]MigrationTask, error) {
	if strings.TrimSpace(batchID) == "" {
		return nil, fmt.Errorf("%w: batch ID is required", ErrInvalidInput)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM admin_migration_tasks
		WHERE batch_id = ? ORDER BY created_at ASC, id ASC`, batchID)
	if err != nil { return nil, fmt.Errorf("list migration batch tasks: %w", err) }
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
	_ = tx.QueryRowContext(ctx, `SELECT request_source, actor FROM admin_migration_audit_events
		WHERE task_id = ? ORDER BY id ASC LIMIT 1`, id).Scan(&task.RequestSource, &task.Actor)
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
	if err := insertMigrationAuditEvent(ctx, tx, task, from, status, message, now); err != nil { return err }
	return tx.Commit()
}

func insertMigrationAuditEvent(ctx context.Context, tx *sql.Tx, task MigrationTask, from, to, message string, at time.Time) error {
	actor := task.Actor
	if actor == "" { actor = "local-api" }
	_, err := tx.ExecContext(ctx, `INSERT INTO admin_migration_audit_events
		(task_id, data_source_id, action, from_status, to_status, actor, request_source, message, occurred_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		task.ID, task.DataSourceID, task.Action, from, to, actor, task.RequestSource, message, at.UTC().Format(time.RFC3339Nano))
	if err != nil { return fmt.Errorf("record migration audit event: %w", err) }
	return nil
}

// ListMigrationAuditEvents returns chronological state changes for one task.
func (s *Store) ListMigrationAuditEvents(ctx context.Context, taskID string) ([]MigrationAuditEvent, error) {
	if _, err := s.GetMigrationTask(ctx, taskID); err != nil { return nil, err }
	rows, err := s.db.QueryContext(ctx, `SELECT e.id, e.task_id, COALESCE(tt.tenant_id, ''), e.data_source_id, e.action, e.from_status, e.to_status,
		e.actor, e.request_source, e.message, e.occurred_at FROM admin_migration_audit_events e
		LEFT JOIN admin_migration_task_tenants tt ON tt.task_id = e.task_id
		WHERE e.task_id = ? ORDER BY e.id ASC`, taskID)
	if err != nil { return nil, fmt.Errorf("list migration audit events: %w", err) }
	defer rows.Close()
	items := make([]MigrationAuditEvent, 0)
	for rows.Next() {
		var item MigrationAuditEvent
		var at string
		if err := rows.Scan(&item.ID, &item.TaskID, &item.TenantID, &item.DataSourceID, &item.Action, &item.FromStatus,
			&item.ToStatus, &item.Actor, &item.RequestSource, &item.Message, &at); err != nil { return nil, err }
		item.OccurredAt, err = time.Parse(time.RFC3339Nano, at)
		if err != nil { return nil, fmt.Errorf("parse migration audit timestamp: %w", err) }
		items = append(items, item)
	}
	return items, rows.Err()
}

// RecoverInterruptedTasks marks tasks left running or queued by a process crash.
func (s *Store) RecoverInterruptedTasks(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil { return fmt.Errorf("begin migration task recovery: %w", err) }
	defer tx.Rollback() //nolint:errcheck
	rows, err := tx.QueryContext(ctx, `SELECT id, data_source_id, action, status,
		COALESCE((SELECT request_source FROM admin_migration_audit_events WHERE task_id = admin_migration_tasks.id ORDER BY id ASC LIMIT 1), ''),
		COALESCE((SELECT actor FROM admin_migration_audit_events WHERE task_id = admin_migration_tasks.id ORDER BY id ASC LIMIT 1), 'local-api')
		FROM admin_migration_tasks WHERE status IN ('queued', 'running')`)
	if err != nil { return fmt.Errorf("find interrupted migration tasks: %w", err) }
	var tasks []MigrationTask
	for rows.Next() {
		var task MigrationTask
		if err := rows.Scan(&task.ID, &task.DataSourceID, &task.Action, &task.Status, &task.RequestSource, &task.Actor); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan interrupted migration task: %w", err)
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil { _ = rows.Close(); return err }
	if err := rows.Close(); err != nil { return err }
	now := time.Now().UTC()
	stamp := now.Format(time.RFC3339Nano)
	for _, task := range tasks {
		result, err := tx.ExecContext(ctx, `UPDATE admin_migration_tasks
			SET status = 'interrupted', message = 'service restarted before task completion', finished_at = ?
			WHERE id = ? AND status = ?`, stamp, task.ID, task.Status)
		if err != nil { return fmt.Errorf("recover migration task %s: %w", task.ID, err) }
		count, err := result.RowsAffected()
		if err != nil { return err }
		if count == 0 { continue }
		if err := insertMigrationAuditEvent(ctx, tx, task, task.Status, "interrupted",
			"service restarted before task completion", now); err != nil { return err }
	}
	return tx.Commit()
}
