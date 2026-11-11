package adminstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrTenantHealthSchedulerLeaseInvalid = errors.New("invalid tenant health scheduler lease")
	ErrTenantHealthSchedulerLeaseLost = errors.New("tenant health scheduler lease is no longer owned")
)

// AcquireTenantHealthSchedulerLease atomically acquires an expired lease or
// renews the lease already owned by ownerID. Lease deadlines use Unix nanoseconds to preserve numeric ordering.
func (s *Store) AcquireTenantHealthSchedulerLease(ctx context.Context, ownerID string, now time.Time, ttl time.Duration) (bool, int64, error) {
	if strings.TrimSpace(ownerID) == "" || len(ownerID) > 128 || now.IsZero() || ttl < 5*time.Second || ttl > 5*time.Minute {
		return false, 0, ErrTenantHealthSchedulerLeaseInvalid
	}
	now = now.UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE admin_tenant_health_scheduler_lease
		SET generation = generation + CASE WHEN owner_id = ? THEN 0 ELSE 1 END,
		    owner_id = ?, lease_until = ?, updated_at = ?
		WHERE id = 1 AND (owner_id = ? OR lease_until <= ? OR owner_id = '')`,
		ownerID, ownerID, now.Add(ttl).UnixNano(), formatTenantHealthTime(now),
		ownerID, now.UnixNano())
	if err != nil { return false, 0, fmt.Errorf("acquire tenant health scheduler lease: %w", err) }
	affected, err := result.RowsAffected()
	if err != nil { return false, 0, fmt.Errorf("read tenant health scheduler lease result: %w", err) }
	if affected == 0 { return false, 0, nil }
	var generation int64
	if err := s.db.QueryRowContext(ctx, "SELECT generation FROM admin_tenant_health_scheduler_lease WHERE id = 1 AND owner_id = ?", ownerID).Scan(&generation); err != nil {
		return false, 0, fmt.Errorf("read acquired tenant health scheduler lease generation: %w", err)
	}
	return true, generation, nil
}

// RenewTenantHealthSchedulerLease extends only a currently valid lease owned by ownerID.
func (s *Store) RenewTenantHealthSchedulerLease(ctx context.Context, ownerID string, now time.Time, ttl time.Duration) (bool, error) {
	if strings.TrimSpace(ownerID) == "" || len(ownerID) > 128 || now.IsZero() || ttl < 5*time.Second || ttl > 5*time.Minute {
		return false, ErrTenantHealthSchedulerLeaseInvalid
	}
	now = now.UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE admin_tenant_health_scheduler_lease
		SET lease_until = ?, updated_at = ?
		WHERE id = 1 AND owner_id = ? AND lease_until > ?`,
		now.Add(ttl).UnixNano(), formatTenantHealthTime(now), ownerID, now.UnixNano())
	if err != nil { return false, fmt.Errorf("renew tenant health scheduler lease: %w", err) }
	affected, err := result.RowsAffected()
	if err != nil { return false, fmt.Errorf("read renewed tenant health scheduler lease result: %w", err) }
	return affected == 1, nil
}

// ReleaseTenantHealthSchedulerLease releases only the lease still owned by ownerID.
func (s *Store) ReleaseTenantHealthSchedulerLease(ctx context.Context, ownerID string) error {
	if strings.TrimSpace(ownerID) == "" || len(ownerID) > 128 { return ErrTenantHealthSchedulerLeaseInvalid }
	_, err := s.db.ExecContext(ctx, `UPDATE admin_tenant_health_scheduler_lease
		SET owner_id = '', lease_until = 0, updated_at = ?
		WHERE id = 1 AND owner_id = ?`, formatTenantHealthTime(time.Now().UTC()), ownerID)
	if err != nil { return fmt.Errorf("release tenant health scheduler lease: %w", err) }
	return nil
}
