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
	TenantID            string     `json:"tenant_id"`
	Status              string     `json:"status"`
	CheckedAt           time.Time  `json:"checked_at"`
	DurationMS          int64      `json:"duration_ms"`
	Message             string     `json:"message"`
	ConsecutiveFailures int64      `json:"consecutive_failures"`
	LastSuccessAt       *time.Time `json:"last_success_at,omitempty"`
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

// TenantHealthSummary combines the latest tenant state with the last 24 hours of checks.
type TenantHealthSummary struct {
	WindowStartedAt         time.Time `json:"window_started_at"`
	TenantCount             int64     `json:"tenant_count"`
	CheckedTenantCount      int64     `json:"checked_tenant_count"`
	HealthyTenantCount      int64     `json:"healthy_tenant_count"`
	UnhealthyTenantCount    int64     `json:"unhealthy_tenant_count"`
	UncheckedTenantCount    int64     `json:"unchecked_tenant_count"`
	ChecksLast24Hours       int64     `json:"checks_last_24_hours"`
	HealthyChecksLast24Hours int64    `json:"healthy_checks_last_24_hours"`
	UnhealthyChecksLast24Hours int64  `json:"unhealthy_checks_last_24_hours"`
	SuccessRateLast24Hours  float64   `json:"success_rate_last_24_hours"`
}

// GetTenantHealthSummary returns current tenant status counts and check outcomes from the last 24 hours.
// TenantHealthTrendBucket contains aggregated check outcomes for one time bucket.
type TenantHealthTrendBucket struct {
	BucketStart time.Time `json:"bucket_start"`
	Checks int64 `json:"checks"`
	Healthy int64 `json:"healthy"`
	Unhealthy int64 `json:"unhealthy"`
	SuccessRate float64 `json:"success_rate"`
}

// TenantHealthTrends is a bounded, zero-filled time series for the health dashboard.
type TenantHealthTrends struct {
	Range string `json:"range"`
	WindowStartedAt time.Time `json:"window_started_at"`
	WindowEndedAt time.Time `json:"window_ended_at"`
	Buckets []TenantHealthTrendBucket `json:"buckets"`
}

// GetTenantHealthTrends returns zero-filled hourly (24h) or daily (7d) check aggregates.
func (s *Store) GetTenantHealthTrends(ctx context.Context, now time.Time, rangeName string) (TenantHealthTrends, error) {
	if now.IsZero() {
		return TenantHealthTrends{}, ErrTenantHealthInvalidInput
	}
	now = now.UTC()
	var bucketCount int
	var bucketSize time.Duration
	var start time.Time
	var groupExpression string
	queryEnd := now
	endExclusive := false
	switch rangeName {
	case "24h":
		bucketCount = 24
		bucketSize = time.Hour
		queryEnd = now.Truncate(time.Hour)
		start = queryEnd.Add(-24 * time.Hour)
		endExclusive = true
		groupExpression = "substr(checked_at, 1, 13)"
	case "7d":
		bucketCount = 7
		bucketSize = 24 * time.Hour
		start = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -6)
		groupExpression = "substr(checked_at, 1, 10)"
	default:
		return TenantHealthTrends{}, ErrTenantHealthInvalidInput
	}

	result := TenantHealthTrends{
		Range: rangeName,
		WindowStartedAt: start,
		WindowEndedAt: queryEnd,
		Buckets: make([]TenantHealthTrendBucket, bucketCount),
	}
	for i := range result.Buckets {
		result.Buckets[i].BucketStart = start.Add(time.Duration(i) * bucketSize)
	}
	endOperator := "<="
	if endExclusive {
		endOperator = "<"
	}
	query := "SELECT " + groupExpression + ", COUNT(*), " +
		"COALESCE(SUM(CASE WHEN status = 'healthy' THEN 1 ELSE 0 END), 0), " +
		"COALESCE(SUM(CASE WHEN status = 'unhealthy' THEN 1 ELSE 0 END), 0) " +
		"FROM admin_tenant_connection_health_history WHERE checked_at >= ? AND checked_at " + endOperator + " ? GROUP BY " + groupExpression
	rows, err := s.db.QueryContext(ctx, query, formatTenantHealthTime(start), formatTenantHealthTime(queryEnd))
	if err != nil {
		return TenantHealthTrends{}, fmt.Errorf("query tenant health trends: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var checks, healthy, unhealthy int64
		if err := rows.Scan(&key, &checks, &healthy, &unhealthy); err != nil {
			return TenantHealthTrends{}, fmt.Errorf("scan tenant health trend bucket: %w", err)
		}
		layout := "2006-01-02T15"
		if rangeName == "7d" {
			layout = "2006-01-02"
		}
		stamp, err := time.ParseInLocation(layout, key, time.UTC)
		if err != nil {
			return TenantHealthTrends{}, fmt.Errorf("parse tenant health trend bucket: %w", err)
		}
		index := int(stamp.Sub(start) / bucketSize)
		if index < 0 || index >= len(result.Buckets) {
			continue
		}
		bucket := &result.Buckets[index]
		bucket.Checks += checks
		bucket.Healthy += healthy
		bucket.Unhealthy += unhealthy
	}
	if err := rows.Err(); err != nil {
		return TenantHealthTrends{}, fmt.Errorf("iterate tenant health trend buckets: %w", err)
	}
	for i := range result.Buckets {
		bucket := &result.Buckets[i]
		if bucket.Checks > 0 {
			bucket.SuccessRate = float64(bucket.Healthy) / float64(bucket.Checks) * 100
		}
	}
	return result, nil
}

func (s *Store) GetTenantHealthSummary(ctx context.Context, now time.Time) (TenantHealthSummary, error) {
	if now.IsZero() {
		return TenantHealthSummary{}, ErrTenantHealthInvalidInput
	}
	now = now.UTC()
	windowStart := now.Add(-24 * time.Hour)
	summary := TenantHealthSummary{WindowStartedAt: windowStart}
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN h.tenant_id IS NOT NULL THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN h.status = 'healthy' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN h.status = 'unhealthy' THEN 1 ELSE 0 END), 0)
		FROM admin_tenants t
		LEFT JOIN admin_tenant_connection_health h ON h.tenant_id = t.id`).Scan(
		&summary.TenantCount, &summary.CheckedTenantCount,
		&summary.HealthyTenantCount, &summary.UnhealthyTenantCount,
	)
	if err != nil {
		return TenantHealthSummary{}, fmt.Errorf("summarize latest tenant health: %w", err)
	}
	summary.UncheckedTenantCount = summary.TenantCount - summary.CheckedTenantCount
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN status = 'healthy' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status = 'unhealthy' THEN 1 ELSE 0 END), 0)
		FROM admin_tenant_connection_health_history WHERE checked_at >= ? AND checked_at <= ?`,
		formatTenantHealthTime(windowStart), formatTenantHealthTime(now),
	).Scan(&summary.ChecksLast24Hours, &summary.HealthyChecksLast24Hours, &summary.UnhealthyChecksLast24Hours)
	if err != nil {
		return TenantHealthSummary{}, fmt.Errorf("summarize tenant health history: %w", err)
	}
	if summary.ChecksLast24Hours > 0 {
		summary.SuccessRateLast24Hours = float64(summary.HealthyChecksLast24Hours) / float64(summary.ChecksLast24Hours) * 100
	}
	return summary, nil
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
	rows, err := s.db.QueryContext(ctx, `SELECT h.tenant_id, h.status, h.checked_at, h.duration_ms, h.message,
		(SELECT COUNT(*) FROM admin_tenant_connection_health_history f
		 WHERE f.tenant_id = h.tenant_id AND f.status = 'unhealthy'
		 AND NOT EXISTS (
			SELECT 1 FROM admin_tenant_connection_health_history ok
			WHERE ok.tenant_id = f.tenant_id AND ok.status = 'healthy'
			AND (ok.checked_at > f.checked_at OR (ok.checked_at = f.checked_at AND ok.id > f.id))
		 )),
		(SELECT ok.checked_at FROM admin_tenant_connection_health_history ok
		 WHERE ok.tenant_id = h.tenant_id AND ok.status = 'healthy'
		 ORDER BY ok.checked_at DESC, ok.id DESC LIMIT 1)
		FROM admin_tenant_connection_health h JOIN admin_tenants t ON t.id = h.tenant_id
		ORDER BY h.checked_at DESC, h.tenant_id`)
	if err != nil { return nil, fmt.Errorf("list tenant connection health: %w", err) }
	defer rows.Close()
	items := make([]TenantConnectionHealth, 0)
	for rows.Next() {
		var item TenantConnectionHealth
		var checked string
		var lastSuccess sql.NullString
		if err := rows.Scan(&item.TenantID, &item.Status, &checked, &item.DurationMS, &item.Message,
			&item.ConsecutiveFailures, &lastSuccess); err != nil {
			return nil, fmt.Errorf("scan tenant connection health: %w", err)
		}
		item.CheckedAt, err = time.Parse(time.RFC3339Nano, checked)
		if err != nil { return nil, fmt.Errorf("parse tenant connection health timestamp: %w", err) }
		if lastSuccess.Valid {
			parsed, parseErr := time.Parse(time.RFC3339Nano, lastSuccess.String)
			if parseErr != nil { return nil, fmt.Errorf("parse last successful tenant health timestamp: %w", parseErr) }
			item.LastSuccessAt = &parsed
		}
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
