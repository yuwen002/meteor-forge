package adminstore

import (
	"context"
	"fmt"
	"time"
)

type OperationAuditEvent struct {
	ID int64 `json:"id"`
	TaskID string `json:"task_id"`
	Kind string `json:"kind"`
	DataSourceID string `json:"data_source_id"`
	TenantID string `json:"tenant_id,omitempty"`
	ShardRuleID string `json:"shard_rule_id,omitempty"`
	Action string `json:"action"`
	FromStatus string `json:"from_status,omitempty"`
	ToStatus string `json:"to_status"`
	Actor string `json:"actor"`
	RequestSource string `json:"request_source,omitempty"`
	Message string `json:"message,omitempty"`
	SourceTable string `json:"source_table,omitempty"`
	TargetTable string `json:"target_table,omitempty"`
	OccurredAt time.Time `json:"occurred_at"`
}

const operationAuditUnion = `
	SELECT e.id, e.task_id, 'migration' AS kind, e.data_source_id,
		COALESCE(tt.tenant_id, '') AS tenant_id, '' AS shard_rule_id,
		e.action, e.from_status, e.to_status, e.actor, e.request_source,
		e.message, '' AS source_table, '' AS target_table, e.occurred_at
	FROM admin_migration_audit_events e
	LEFT JOIN admin_migration_task_tenants tt ON tt.task_id = e.task_id
	UNION ALL
	SELECT e.id, e.task_id, 'shard_ddl' AS kind, e.data_source_id,
		'' AS tenant_id, e.shard_rule_id, e.action, e.from_status,
		e.to_status, e.actor, e.request_source, e.message,
		t.source_table, t.target_table, e.occurred_at
	FROM admin_shard_ddl_audit_events e
	LEFT JOIN admin_shard_ddl_tasks t ON t.id = e.task_id
`

func (s *Store) ListOperationAuditEvents(ctx context.Context, limit, offset int, kind, status string) ([]OperationAuditEvent, int, error) {
	if limit <= 0 || limit > 200 { limit = 50 }
	if offset < 0 { offset = 0 }
	filtered := " FROM (" + operationAuditUnion + ") AS events WHERE (? = '' OR kind = ?) AND (? = '' OR to_status = ?)"
	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*)"+filtered, kind, kind, status, status).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count operation audit events: %w", err)
	}
	query := "SELECT id, task_id, kind, data_source_id, tenant_id, shard_rule_id, action, from_status, to_status, actor, request_source, message, source_table, target_table, occurred_at" +
		filtered + " ORDER BY occurred_at DESC, kind ASC, id DESC LIMIT ? OFFSET ?"
	rows, err := s.db.QueryContext(ctx, query, kind, kind, status, status, limit, offset)
	if err != nil { return nil, 0, fmt.Errorf("list operation audit events: %w", err) }
	defer rows.Close()
	items := make([]OperationAuditEvent, 0)
	for rows.Next() {
		var item OperationAuditEvent
		var occurred string
		if err := rows.Scan(&item.ID, &item.TaskID, &item.Kind, &item.DataSourceID, &item.TenantID,
			&item.ShardRuleID, &item.Action, &item.FromStatus, &item.ToStatus, &item.Actor,
			&item.RequestSource, &item.Message, &item.SourceTable, &item.TargetTable, &occurred); err != nil {
			return nil, 0, err
		}
		item.OccurredAt, err = time.Parse(time.RFC3339Nano, occurred)
		if err != nil { return nil, 0, fmt.Errorf("parse operation audit timestamp: %w", err) }
		items = append(items, item)
	}
	if err := rows.Err(); err != nil { return nil, 0, err }
	return items, total, nil
}
