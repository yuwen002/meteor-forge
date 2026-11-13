package adminstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrTenantHealthSchedulerRunNotFound = errors.New("tenant health scheduler run not found")
var ErrTenantHealthSchedulerRunFinalized = errors.New("tenant health scheduler run is already finalized")

type TenantHealthSchedulerRun struct {
	ID             int64      `json:"id"`
	LeaseGeneration int64     `json:"lease_generation"`
	StartedAt      time.Time  `json:"started_at"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
	DurationMS     int64      `json:"duration_ms"`
	Status         string     `json:"status"`
	ErrorCode      string     `json:"error_code,omitempty"`
	ErrorMessage   string     `json:"error_message,omitempty"`
}

type TenantHealthSchedulerRunFilter struct {
	Status string
	From   *time.Time
	To     *time.Time
	Limit  int
	Offset int
}

type TenantHealthSchedulerRunPage struct {
	Items []TenantHealthSchedulerRun `json:"items"`
	Total int64                     `json:"total"`
}

// StartTenantHealthSchedulerRun records a round only after its instance owns a lease.
func (s *Store) StartTenantHealthSchedulerRun(ctx context.Context, generation int64, startedAt time.Time) (int64, error) {
	if generation <= 0 || startedAt.IsZero() {
		return 0, ErrTenantHealthSchedulerLeaseInvalid
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO admin_tenant_health_scheduler_runs
		(lease_generation, started_at, status) VALUES (?, ?, 'running')`,
		generation, formatTenantHealthTime(startedAt))
	if err != nil {
		return 0, fmt.Errorf("start tenant health scheduler run: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read tenant health scheduler run ID: %w", err)
	}
	return id, nil
}

// FinishTenantHealthSchedulerRun finalizes one run with a whitelisted error classification.
func (s *Store) FinishTenantHealthSchedulerRun(ctx context.Context, id int64, finishedAt time.Time, durationMS int64, status, errorCode string) error {
	if id <= 0 || finishedAt.IsZero() || durationMS < 0 {
		return ErrTenantHealthInvalidInput
	}
	message := ""
	switch status {
	case "succeeded":
		errorCode = ""
	case "failed":
		switch errorCode {
		case "tenant_checks_failed":
			message = "一个或多个租户巡检失败"
		case "canceled":
			message = "巡检因服务关闭或请求取消而中断"
		default:
			errorCode = "unknown"
			message = "巡检执行失败"
		}
	case "lease_lost":
		errorCode = "lease_lost"
		message = "租约已丢失，旧实例停止本轮"
	default:
		return ErrTenantHealthInvalidInput
	}
	result, err := s.db.ExecContext(ctx, `UPDATE admin_tenant_health_scheduler_runs
		SET finished_at = ?, duration_ms = ?, status = ?, error_code = ?, error_message = ?
		WHERE id = ? AND status = 'running'`,
		formatTenantHealthTime(finishedAt), durationMS, status, errorCode, message, id)
	if err != nil {
		return fmt.Errorf("finish tenant health scheduler run: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read finalized tenant health scheduler run count: %w", err)
	}
	if affected == 0 {
		var exists int
		if err := s.db.QueryRowContext(ctx, "SELECT 1 FROM admin_tenant_health_scheduler_runs WHERE id = ?", id).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
			return ErrTenantHealthSchedulerRunNotFound
		} else if err != nil {
			return fmt.Errorf("check tenant health scheduler run: %w", err)
		}
		return ErrTenantHealthSchedulerRunFinalized
	}
	return nil
}

// RecoverStaleTenantHealthSchedulerRuns marks unfinished runs from older lease generations as interrupted.
func (s *Store) RecoverStaleTenantHealthSchedulerRuns(ctx context.Context, currentGeneration int64, recoveredAt time.Time) (int64, error) {
	if currentGeneration <= 0 || recoveredAt.IsZero() {
		return 0, ErrTenantHealthSchedulerLeaseInvalid
	}
	result, err := s.db.ExecContext(ctx, `UPDATE admin_tenant_health_scheduler_runs
		SET finished_at = ?, status = 'interrupted', error_code = 'interrupted',
			error_message = '实例退出后租约已被新实例接管'
		WHERE status = 'running' AND lease_generation < ?`,
		formatTenantHealthTime(recoveredAt), currentGeneration)
	if err != nil {
		return 0, fmt.Errorf("recover stale tenant health scheduler runs: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count recovered tenant health scheduler runs: %w", err)
	}
	return affected, nil
}

// ListTenantHealthSchedulerRuns returns a bounded, newest-first page of durable scheduler rounds.
func (s *Store) ListTenantHealthSchedulerRuns(ctx context.Context, filter TenantHealthSchedulerRunFilter) (TenantHealthSchedulerRunPage, error) {
	if filter.Status != "" {
		switch filter.Status {
		case "running", "succeeded", "failed", "lease_lost", "interrupted":
		default:
			return TenantHealthSchedulerRunPage{}, ErrTenantHealthInvalidInput
		}
	}
	if filter.From != nil && filter.To != nil && filter.From.After(*filter.To) {
		return TenantHealthSchedulerRunPage{}, ErrTenantHealthInvalidInput
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 || filter.Offset < 0 || filter.Offset > 1000000 {
		return TenantHealthSchedulerRunPage{}, ErrTenantHealthInvalidInput
	}
	conditions := make([]string, 0, 3)
	args := make([]any, 0, 3)
	if filter.Status != "" {
		conditions = append(conditions, "status = ?")
		args = append(args, filter.Status)
	}
	if filter.From != nil {
		conditions = append(conditions, "started_at >= ?")
		args = append(args, formatTenantHealthTime(*filter.From))
	}
	if filter.To != nil {
		conditions = append(conditions, "started_at <= ?")
		args = append(args, formatTenantHealthTime(*filter.To))
	}
	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}
	var total int64
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM admin_tenant_health_scheduler_runs"+where, args...).Scan(&total); err != nil {
		return TenantHealthSchedulerRunPage{}, fmt.Errorf("count tenant health scheduler runs: %w", err)
	}
	queryArgs := append(append([]any(nil), args...), limit, filter.Offset)
	rows, err := s.db.QueryContext(ctx, `SELECT id, lease_generation, started_at, finished_at, duration_ms, status, error_code, error_message
		FROM admin_tenant_health_scheduler_runs`+where+` ORDER BY started_at DESC, id DESC LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return TenantHealthSchedulerRunPage{}, fmt.Errorf("list tenant health scheduler runs: %w", err)
	}
	defer rows.Close()
	items := make([]TenantHealthSchedulerRun, 0)
	for rows.Next() {
		var item TenantHealthSchedulerRun
		var started string
		var finished sql.NullString
		if err := rows.Scan(&item.ID, &item.LeaseGeneration, &started, &finished, &item.DurationMS, &item.Status, &item.ErrorCode, &item.ErrorMessage); err != nil {
			return TenantHealthSchedulerRunPage{}, fmt.Errorf("scan tenant health scheduler run: %w", err)
		}
		item.StartedAt, err = time.Parse(time.RFC3339Nano, started)
		if err != nil {
			return TenantHealthSchedulerRunPage{}, fmt.Errorf("parse tenant health scheduler run start: %w", err)
		}
		if finished.Valid {
			value, parseErr := time.Parse(time.RFC3339Nano, finished.String)
			if parseErr != nil {
				return TenantHealthSchedulerRunPage{}, fmt.Errorf("parse tenant health scheduler run finish: %w", parseErr)
			}
			item.FinishedAt = &value
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return TenantHealthSchedulerRunPage{}, fmt.Errorf("iterate tenant health scheduler runs: %w", err)
	}
	return TenantHealthSchedulerRunPage{Items: items, Total: total}, nil
}
