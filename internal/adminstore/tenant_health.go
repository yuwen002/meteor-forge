package adminstore

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

var ErrTenantHealthInvalidInput = errors.New("invalid tenant health input")

// TenantConnectionHealth is the latest persisted connection-check result for a tenant.
type TenantConnectionHealth struct {
	TenantID   string    `json:"tenant_id"`
	Status     string    `json:"status"`
	CheckedAt  time.Time `json:"checked_at"`
	DurationMS int64     `json:"duration_ms"`
	Message    string    `json:"message"`
}

// TenantHealthSettings controls the optional single-instance scheduled checker.
type TenantHealthSettings struct {
	Enabled              bool      `json:"enabled"`
	IntervalSeconds      int       `json:"interval_seconds"`
	TimeoutSeconds       int       `json:"timeout_seconds"`
	HistoryRetentionDays int       `json:"history_retention_days"`
	UpdatedAt            time.Time `json:"updated_at"`
}

// TenantConnectionHealthHistory is an immutable normalized check result.
type TenantConnectionHealthHistory struct {
	ID         int64     `json:"id"`
	TenantID   string    `json:"tenant_id"`
	Source     string    `json:"source"`
	Status     string    `json:"status"`
	CheckedAt  time.Time `json:"checked_at"`
	DurationMS int64     `json:"duration_ms"`
	Message    string    `json:"message"`
}

// TenantHealthHistoryFilter uses a stable (checked_at, id) descending cursor.
type TenantHealthHistoryFilter struct {
	TenantID string
	Status   string
	Source   string
	From     *time.Time
	To       *time.Time
	Limit    int
	Cursor   string
}

// TenantHealthHistoryPage is one bounded page of check history.
type TenantHealthHistoryPage struct {
	Items      []TenantConnectionHealthHistory `json:"items"`
	NextCursor string                          `json:"next_cursor,omitempty"`
}

func validateTenantHealthSettings(item TenantHealthSettings) error {
	if item.IntervalSeconds < 30 || item.IntervalSeconds > 86400 {
		return fmt.Errorf("%w: interval_seconds must be between 30 and 86400", ErrTenantHealthInvalidInput)
	}
	if item.TimeoutSeconds < 1 || item.TimeoutSeconds > 30 {
		return fmt.Errorf("%w: timeout_seconds must be between 1 and 30", ErrTenantHealthInvalidInput)
	}
	if item.HistoryRetentionDays < 1 || item.HistoryRetentionDays > 365 {
		return fmt.Errorf("%w: history_retention_days must be between 1 and 365", ErrTenantHealthInvalidInput)
	}
	return nil
}

// GetTenantHealthSettings returns the persisted global scheduling configuration.
func (s *Store) GetTenantHealthSettings(ctx context.Context) (TenantHealthSettings, error) {
	var item TenantHealthSettings
	var enabled int
	var updated string
	err := s.db.QueryRowContext(ctx, `SELECT enabled, interval_seconds, timeout_seconds, history_retention_days, updated_at
		FROM admin_tenant_health_settings WHERE id = 1`).Scan(&enabled, &item.IntervalSeconds, &item.TimeoutSeconds, &item.HistoryRetentionDays, &updated)
	if err != nil {
		return TenantHealthSettings{}, fmt.Errorf("get tenant health settings: %w", err)
	}
	item.Enabled = enabled == 1
	item.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return TenantHealthSettings{}, fmt.Errorf("parse tenant health settings timestamp: %w", err)
	}
	return item, nil
}

// UpdateTenantHealthSettings validates and atomically replaces the complete settings row.
func (s *Store) UpdateTenantHealthSettings(ctx context.Context, item TenantHealthSettings) (TenantHealthSettings, error) {
	if err := validateTenantHealthSettings(item); err != nil {
		return TenantHealthSettings{}, err
	}
	item.UpdatedAt = time.Now().UTC()
	enabled := 0
	if item.Enabled { enabled = 1 }
	_, err := s.db.ExecContext(ctx, `UPDATE admin_tenant_health_settings SET enabled = ?, interval_seconds = ?,
		timeout_seconds = ?, history_retention_days = ?, updated_at = ? WHERE id = 1`,
		enabled, item.IntervalSeconds, item.TimeoutSeconds, item.HistoryRetentionDays, item.UpdatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return TenantHealthSettings{}, fmt.Errorf("update tenant health settings: %w", err)
	}
	return item, nil
}

// RecordTenantConnectionHealth keeps the legacy manual-check behavior and also appends history.
func (s *Store) RecordTenantConnectionHealth(ctx context.Context, item TenantConnectionHealth) error {
	return s.RecordTenantConnectionHealthWithSource(ctx, item, "manual")
}

// RecordTenantConnectionHealthWithSource appends history and updates the latest snapshot in one transaction.
func (s *Store) RecordTenantConnectionHealthWithSource(ctx context.Context, item TenantConnectionHealth, source string) error {
	if item.TenantID == "" || (item.Status != "healthy" && item.Status != "unhealthy") ||
		item.CheckedAt.IsZero() || item.DurationMS < 0 || (source != "manual" && source != "scheduled") {
		return ErrTenantHealthInvalidInput
	}
	item.Message = normalizeTenantHealthMessage(item.Message)
	checkedAt := formatTenantHealthTime(item.CheckedAt)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tenant health result transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var exists int
	if err := tx.QueryRowContext(ctx, "SELECT 1 FROM admin_tenants WHERE id = ?", item.TenantID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return ErrTenantNotFound
	} else if err != nil {
		return fmt.Errorf("verify tenant for connection health: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO admin_tenant_connection_health_history
		(tenant_id, source, status, checked_at, duration_ms, message) VALUES (?, ?, ?, ?, ?, ?)`,
		item.TenantID, source, item.Status, checkedAt, item.DurationMS, item.Message); err != nil {
		return fmt.Errorf("append tenant connection health history: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO admin_tenant_connection_health
		(tenant_id, status, checked_at, duration_ms, message) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(tenant_id) DO UPDATE SET status = excluded.status, checked_at = excluded.checked_at,
			duration_ms = excluded.duration_ms, message = excluded.message`,
		item.TenantID, item.Status, checkedAt, item.DurationMS, item.Message); err != nil {
		return fmt.Errorf("update latest tenant connection health: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tenant connection health result: %w", err)
	}
	return nil
}

func normalizeTenantHealthMessage(message string) string {
	switch message {
	case "connection successful", "connection check timed out", "connection check canceled",
		"connection check canceled before it started", "connection check is unavailable",
		"connection check failed; verify the registered data source":
		return message
	default:
		return "connection check failed; verify the registered data source"
	}
}

// ListTenantConnectionHealth returns the latest health result for each registered tenant.
func (s *Store) ListTenantConnectionHealth(ctx context.Context) ([]TenantConnectionHealth, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT h.tenant_id, h.status, h.checked_at, h.duration_ms, h.message
		FROM admin_tenant_connection_health h JOIN admin_tenants t ON t.id = h.tenant_id
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

// ListTenantConnectionHealthHistory returns a stable descending page (maximum 100 rows).
func (s *Store) ListTenantConnectionHealthHistory(ctx context.Context, filter TenantHealthHistoryFilter) (TenantHealthHistoryPage, error) {
	if filter.Status != "" && filter.Status != "healthy" && filter.Status != "unhealthy" {
		return TenantHealthHistoryPage{}, ErrTenantHealthInvalidInput
	}
	if filter.Source != "" && filter.Source != "manual" && filter.Source != "scheduled" {
		return TenantHealthHistoryPage{}, ErrTenantHealthInvalidInput
	}
	if filter.From != nil && filter.To != nil && filter.From.After(*filter.To) {
		return TenantHealthHistoryPage{}, ErrTenantHealthInvalidInput
	}
	limit := filter.Limit
	if limit <= 0 { limit = 50 }
	if limit > 100 { limit = 100 }
	conditions := make([]string, 0, 6)
	args := make([]any, 0, 8)
	if filter.TenantID != "" { conditions = append(conditions, "tenant_id = ?"); args = append(args, filter.TenantID) }
	if filter.Status != "" { conditions = append(conditions, "status = ?"); args = append(args, filter.Status) }
	if filter.Source != "" { conditions = append(conditions, "source = ?"); args = append(args, filter.Source) }
	if filter.From != nil { conditions = append(conditions, "checked_at >= ?"); args = append(args, formatTenantHealthTime(*filter.From)) }
	if filter.To != nil { conditions = append(conditions, "checked_at <= ?"); args = append(args, formatTenantHealthTime(*filter.To)) }
	if filter.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(filter.Cursor)
		if err != nil { return TenantHealthHistoryPage{}, ErrTenantHealthInvalidInput }
		parts := strings.SplitN(string(raw), "\n", 2)
		if len(parts) != 2 || parts[1] == "" { return TenantHealthHistoryPage{}, ErrTenantHealthInvalidInput }
		cursorTime, err := time.Parse(time.RFC3339Nano, parts[0])
		if err != nil { return TenantHealthHistoryPage{}, ErrTenantHealthInvalidInput }
		cursorID, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || cursorID <= 0 { return TenantHealthHistoryPage{}, ErrTenantHealthInvalidInput }
		conditions = append(conditions, "(checked_at < ? OR (checked_at = ? AND id < ?))")
		stamp := formatTenantHealthTime(cursorTime)
		args = append(args, stamp, stamp, cursorID)
	}
	query := "SELECT id, tenant_id, source, status, checked_at, duration_ms, message FROM admin_tenant_connection_health_history"
	if len(conditions) > 0 { query += " WHERE " + strings.Join(conditions, " AND ") }
	query += " ORDER BY checked_at DESC, id DESC LIMIT ?"
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil { return TenantHealthHistoryPage{}, fmt.Errorf("query tenant connection health history: %w", err) }
	defer rows.Close()
	items := make([]TenantConnectionHealthHistory, 0, limit+1)
	for rows.Next() {
		var item TenantConnectionHealthHistory
		var checked string
		if err := rows.Scan(&item.ID, &item.TenantID, &item.Source, &item.Status, &checked, &item.DurationMS, &item.Message); err != nil {
			return TenantHealthHistoryPage{}, fmt.Errorf("scan tenant connection health history: %w", err)
		}
		item.CheckedAt, err = time.Parse(time.RFC3339Nano, checked)
		if err != nil { return TenantHealthHistoryPage{}, fmt.Errorf("parse tenant health history timestamp: %w", err) }
		items = append(items, item)
	}
	if err := rows.Err(); err != nil { return TenantHealthHistoryPage{}, fmt.Errorf("iterate tenant connection health history: %w", err) }
	page := TenantHealthHistoryPage{Items: items}
	if len(items) > limit {
		last := items[limit-1]
		page.Items = items[:limit]
		cursor := formatTenantHealthTime(last.CheckedAt) + "\n" + strconv.FormatInt(last.ID, 10)
		page.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(cursor))
	}
	return page, nil
}

// DeleteExpiredTenantConnectionHealthHistory removes at most limit expired rows per call.
func (s *Store) DeleteExpiredTenantConnectionHealthHistory(ctx context.Context, before time.Time, limit int) (int64, error) {
	if before.IsZero() { return 0, ErrTenantHealthInvalidInput }
	if limit <= 0 { limit = 500 }
	if limit > 5000 { limit = 5000 }
	result, err := s.db.ExecContext(ctx, `DELETE FROM admin_tenant_connection_health_history
		WHERE id IN (SELECT id FROM admin_tenant_connection_health_history
			WHERE checked_at < ? ORDER BY checked_at ASC, id ASC LIMIT ?)`,
		formatTenantHealthTime(before), limit)
	if err != nil { return 0, fmt.Errorf("delete expired tenant health history: %w", err) }
	affected, err := result.RowsAffected()
	if err != nil { return 0, fmt.Errorf("count deleted tenant health history: %w", err) }
	return affected, nil
}


// formatTenantHealthTime uses fixed-width UTC precision so lexical ordering matches chronological ordering.
func formatTenantHealthTime(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000000000Z")
}
