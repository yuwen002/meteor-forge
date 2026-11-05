package adminstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ShardDDLTask is a durable physical-table operation. It never stores SQL or credentials.
type ShardDDLTask struct {
	ID           string     `json:"id"`
	ShardRuleID  string     `json:"shard_rule_id"`
	DataSourceID string     `json:"data_source_id"`
	Action       string     `json:"action"`
	SourceTable  string     `json:"source_table"`
	TargetTable  string     `json:"target_table"`
	Status       string     `json:"status"`
	Message      string     `json:"message,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	RequestSource string    `json:"-"`
}

type ShardDDLAuditEvent struct {
	ID           int64     `json:"id"`
	TaskID       string    `json:"task_id"`
	ShardRuleID  string    `json:"shard_rule_id"`
	DataSourceID string    `json:"data_source_id"`
	Action       string    `json:"action"`
	FromStatus   string    `json:"from_status,omitempty"`
	ToStatus     string    `json:"to_status"`
	Actor        string    `json:"actor"`
	RequestSource string   `json:"request_source,omitempty"`
	Message      string    `json:"message,omitempty"`
	OccurredAt   time.Time `json:"occurred_at"`
}

var ErrShardDDLTaskNotFound = errors.New("shard DDL task not found")

// CreateShardDDLTask atomically stores a queued task and its initial audit event.
func (s *Store) CreateShardDDLTask(ctx context.Context, task ShardDDLTask) (ShardDDLTask, error) {
	if task.ShardRuleID == "" || task.DataSourceID == "" || task.SourceTable == "" || task.TargetTable == "" || task.Action != "clone" {
		return ShardDDLTask{}, fmt.Errorf("%w: invalid shard DDL task", ErrInvalidInput)
	}
	id, err := newID()
	if err != nil { return ShardDDLTask{}, err }
	now := time.Now().UTC()
	task.ID, task.Status, task.CreatedAt = id, "queued", now
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil { return ShardDDLTask{}, fmt.Errorf("begin shard DDL task: %w", err) }
	defer tx.Rollback() //nolint:errcheck
	var dataSourceID string
	if err := tx.QueryRowContext(ctx, "SELECT data_source_id FROM admin_shard_rules WHERE id = ?", task.ShardRuleID).Scan(&dataSourceID); errors.Is(err, sql.ErrNoRows) {
		return ShardDDLTask{}, ErrShardRuleNotFound
	} else if err != nil { return ShardDDLTask{}, fmt.Errorf("verify shard rule for DDL task: %w", err) }
	if dataSourceID != task.DataSourceID { return ShardDDLTask{}, ErrShardRuleDataSourceNotFound }
	_, err = tx.ExecContext(ctx, `INSERT INTO admin_shard_ddl_tasks
		(id, shard_rule_id, data_source_id, action, source_table, target_table, status, message, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 'queued', '', ?)`,
		task.ID, task.ShardRuleID, task.DataSourceID, task.Action, task.SourceTable, task.TargetTable, now.Format(time.RFC3339Nano))
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), ErrActiveTasks.Error()) { return ShardDDLTask{}, ErrActiveTasks }
		return ShardDDLTask{}, fmt.Errorf("insert shard DDL task: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO admin_shard_ddl_audit_events
		(task_id, shard_rule_id, data_source_id, action, from_status, to_status, actor, request_source, message, occurred_at)
		VALUES (?, ?, ?, ?, '', 'queued', 'local-api', ?, 'DDL task submitted', ?)`,
		task.ID, task.ShardRuleID, task.DataSourceID, task.Action, task.RequestSource, now.Format(time.RFC3339Nano)); err != nil {
		return ShardDDLTask{}, fmt.Errorf("audit shard DDL task submission: %w", err)
	}
	if err := tx.Commit(); err != nil { return ShardDDLTask{}, fmt.Errorf("commit shard DDL task: %w", err) }
	return task, nil
}

func (s *Store) GetShardDDLTask(ctx context.Context, id string) (ShardDDLTask, error) {
	var task ShardDDLTask
	var created string
	var started, finished sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id, shard_rule_id, data_source_id, action, source_table, target_table,
		status, message, created_at, started_at, finished_at FROM admin_shard_ddl_tasks WHERE id = ?`, id).
		Scan(&task.ID, &task.ShardRuleID, &task.DataSourceID, &task.Action, &task.SourceTable, &task.TargetTable,
			&task.Status, &task.Message, &created, &started, &finished)
	if errors.Is(err, sql.ErrNoRows) { return ShardDDLTask{}, ErrShardDDLTaskNotFound }
	if err != nil { return ShardDDLTask{}, fmt.Errorf("get shard DDL task: %w", err) }
	task.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil { return ShardDDLTask{}, err }
	if started.Valid { value, e := time.Parse(time.RFC3339Nano, started.String); if e != nil { return ShardDDLTask{}, e }; task.StartedAt = &value }
	if finished.Valid { value, e := time.Parse(time.RFC3339Nano, finished.String); if e != nil { return ShardDDLTask{}, e }; task.FinishedAt = &value }
	return task, nil
}

// UpdateShardDDLTask changes state and appends its audit event in one transaction.
func (s *Store) UpdateShardDDLTask(ctx context.Context, id, status, message string, started, finished *time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil { return fmt.Errorf("begin shard DDL task update: %w", err) }
	defer tx.Rollback() //nolint:errcheck
	var task ShardDDLTask
	var created string
	var oldStarted, oldFinished sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT id, shard_rule_id, data_source_id, action, status, created_at, started_at, finished_at
		FROM admin_shard_ddl_tasks WHERE id = ?`, id).Scan(&task.ID, &task.ShardRuleID, &task.DataSourceID, &task.Action,
		&task.Status, &created, &oldStarted, &oldFinished)
	if errors.Is(err, sql.ErrNoRows) { return ErrShardDDLTaskNotFound }
	if err != nil { return err }
	var requestSource string
	_ = tx.QueryRowContext(ctx, "SELECT request_source FROM admin_shard_ddl_audit_events WHERE task_id = ? ORDER BY id ASC LIMIT 1", id).Scan(&requestSource)
	valid := (task.Status == "queued" && (status == "running" || status == "failed" || status == "canceled" || status == "interrupted")) ||
		(task.Status == "running" && (status == "succeeded" || status == "failed" || status == "canceled" || status == "interrupted"))
	if !valid { return ErrInvalidTaskTransition }
	if started == nil && oldStarted.Valid { value, e := time.Parse(time.RFC3339Nano, oldStarted.String); if e != nil { return e }; started = &value }
	var startedValue any
	if started != nil { startedValue = started.UTC().Format(time.RFC3339Nano) }
	var finishedValue any
	if finished != nil { finishedValue = finished.UTC().Format(time.RFC3339Nano) }
	result, err := tx.ExecContext(ctx, `UPDATE admin_shard_ddl_tasks SET status = ?, message = ?,
		started_at = COALESCE(?, started_at), finished_at = ? WHERE id = ? AND status = ?`,
		status, message, startedValue, finishedValue, id, task.Status)
	if err != nil { return fmt.Errorf("update shard DDL task: %w", err) }
	count, err := result.RowsAffected()
	if err != nil { return err }
	if count != 1 { return ErrInvalidTaskTransition }
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO admin_shard_ddl_audit_events
		(task_id, shard_rule_id, data_source_id, action, from_status, to_status, actor, request_source, message, occurred_at)
		VALUES (?, ?, ?, ?, ?, ?, 'local-api', ?, ?, ?)`,
		task.ID, task.ShardRuleID, task.DataSourceID, task.Action, task.Status, status, requestSource, message, now); err != nil {
		return fmt.Errorf("audit shard DDL task transition: %w", err)
	}
	return tx.Commit()
}

func (s *Store) ListShardDDLTasks(ctx context.Context, limit int) ([]ShardDDLTask, error) {
	if limit <= 0 || limit > 200 { limit = 50 }
	rows, err := s.db.QueryContext(ctx, "SELECT id FROM admin_shard_ddl_tasks ORDER BY created_at DESC LIMIT ?", limit)
	if err != nil { return nil, err }
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() { var id string; if err := rows.Scan(&id); err != nil { return nil, err }; ids = append(ids, id) }
	if err := rows.Err(); err != nil { return nil, err }
	items := make([]ShardDDLTask, 0, len(ids))
	for _, id := range ids { item, err := s.GetShardDDLTask(ctx, id); if err != nil { return nil, err }; items = append(items, item) }
	return items, nil
}

func (s *Store) ListShardDDLAuditEvents(ctx context.Context, taskID string) ([]ShardDDLAuditEvent, error) {
	if _, err := s.GetShardDDLTask(ctx, taskID); err != nil { return nil, err }
	rows, err := s.db.QueryContext(ctx, `SELECT id, task_id, shard_rule_id, data_source_id, action, from_status, to_status,
		actor, request_source, message, occurred_at FROM admin_shard_ddl_audit_events WHERE task_id = ? ORDER BY id ASC`, taskID)
	if err != nil { return nil, err }
	defer rows.Close()
	items := make([]ShardDDLAuditEvent, 0)
	for rows.Next() {
		var item ShardDDLAuditEvent
		var occurred string
		if err := rows.Scan(&item.ID, &item.TaskID, &item.ShardRuleID, &item.DataSourceID, &item.Action,
			&item.FromStatus, &item.ToStatus, &item.Actor, &item.RequestSource, &item.Message, &occurred); err != nil { return nil, err }
		item.OccurredAt, err = time.Parse(time.RFC3339Nano, occurred)
		if err != nil { return nil, err }
		items = append(items, item)
	}
	return items, rows.Err()
}

// RecoverInterruptedShardDDLTasks marks tasks left queued/running after a process restart.
func (s *Store) RecoverInterruptedShardDDLTasks(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil { return err }
	defer tx.Rollback() //nolint:errcheck
	rows, err := tx.QueryContext(ctx, "SELECT t.id, t.shard_rule_id, t.data_source_id, t.action, t.status, COALESCE((SELECT request_source FROM admin_shard_ddl_audit_events WHERE task_id = t.id ORDER BY id ASC LIMIT 1), '') FROM admin_shard_ddl_tasks t WHERE t.status IN ('queued','running')")
	if err != nil { return err }
	type pending struct { id, ruleID, sourceID, action, status, requestSource string }
	items := make([]pending, 0)
	for rows.Next() { var item pending; if err := rows.Scan(&item.id, &item.ruleID, &item.sourceID, &item.action, &item.status, &item.requestSource); err != nil { rows.Close(); return err }; items = append(items, item) }
	if err := rows.Err(); err != nil { rows.Close(); return err }
	if err := rows.Close(); err != nil { return err }
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, item := range items {
		if _, err := tx.ExecContext(ctx, "UPDATE admin_shard_ddl_tasks SET status = 'interrupted', message = 'service restarted; inspect target schema before retrying', finished_at = ? WHERE id = ? AND status = ?", now, item.id, item.status); err != nil { return err }
		if _, err := tx.ExecContext(ctx, `INSERT INTO admin_shard_ddl_audit_events
			(task_id, shard_rule_id, data_source_id, action, from_status, to_status, actor, request_source, message, occurred_at)
			VALUES (?, ?, ?, ?, ?, 'interrupted', 'local-api', ?, 'service restarted; inspect target schema before retrying', ?)`,
			item.id, item.ruleID, item.sourceID, item.action, item.status, item.requestSource, now); err != nil { return err }
	}
	return tx.Commit()
}
