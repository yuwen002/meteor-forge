package adminstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// TenantConnectionHealth is the latest persisted connection-check result for a tenant.
type TenantConnectionHealth struct {
	TenantID   string    `json:"tenant_id"`
	Status     string    `json:"status"`
	CheckedAt  time.Time `json:"checked_at"`
	DurationMS int64     `json:"duration_ms"`
	Message    string    `json:"message"`
}

// RecordTenantConnectionHealth stores only normalized, non-secret check metadata.
func (s *Store) RecordTenantConnectionHealth(ctx context.Context, item TenantConnectionHealth) error {
	if item.TenantID == "" || (item.Status != "healthy" && item.Status != "unhealthy") || item.CheckedAt.IsZero() || item.DurationMS < 0 {
		return errors.New("invalid tenant connection health result")
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, "SELECT 1 FROM admin_tenants WHERE id = ?", item.TenantID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return ErrTenantNotFound
	} else if err != nil {
		return fmt.Errorf("verify tenant for connection health: %w", err)
	}
	switch item.Message {
	case "connection successful", "connection check timed out", "connection check canceled",
		"connection check canceled before it started", "connection check is unavailable",
		"connection check failed; verify the registered data source":
	default:
		item.Message = "connection check failed; verify the registered data source"
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO admin_tenant_connection_health
		(tenant_id, status, checked_at, duration_ms, message)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(tenant_id) DO UPDATE SET
			status = excluded.status,
			checked_at = excluded.checked_at,
			duration_ms = excluded.duration_ms,
			message = excluded.message`,
		item.TenantID, item.Status, item.CheckedAt.UTC().Format(time.RFC3339Nano), item.DurationMS, item.Message)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) { return ErrTenantNotFound }
		return fmt.Errorf("record tenant connection health: %w", err)
	}
	return nil
}

// ListTenantConnectionHealth returns the latest health result for each registered tenant.
func (s *Store) ListTenantConnectionHealth(ctx context.Context) ([]TenantConnectionHealth, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT h.tenant_id, h.status, h.checked_at, h.duration_ms, h.message
		FROM admin_tenant_connection_health h
		JOIN admin_tenants t ON t.id = h.tenant_id
		ORDER BY h.checked_at DESC, h.tenant_id`)
	if err != nil { return nil, fmt.Errorf("list tenant connection health: %w", err) }
	defer rows.Close()
	items := make([]TenantConnectionHealth, 0)
	for rows.Next() {
		var item TenantConnectionHealth
		var checked string
		if err := rows.Scan(&item.TenantID, &item.Status, &checked, &item.DurationMS, &item.Message); err != nil {
			return nil, fmt.Errorf("scan tenant connection health: %w", err)
		}
		item.CheckedAt, err = time.Parse(time.RFC3339Nano, checked)
		if err != nil { return nil, fmt.Errorf("parse tenant connection health timestamp: %w", err) }
		items = append(items, item)
	}
	if err := rows.Err(); err != nil { return nil, fmt.Errorf("iterate tenant connection health: %w", err) }
	return items, nil
}
