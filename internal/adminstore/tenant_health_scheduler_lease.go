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
// renews the lease already owned by ownerID. Lease deadlines use Unix nanoseconds
// to preserve numeric ordering; validity is based on live time at execution.
func (s *Store) AcquireTenantHealthSchedulerLease(ctx context.Context, ownerID string, now time.Time, ttl time.Duration) (bool, int64, error) {
	if strings.TrimSpace(ownerID) == "" || len(ownerID) > 128 || now.IsZero() || ttl < 5*time.Second || ttl > 5*time.Minute {
		return false, 0, ErrTenantHealthSchedulerLeaseInvalid
	}
	return s.acquireTenantHealthSchedulerLeaseWithClock(ctx, ownerID, time.Now, ttl)
}

// acquireTenantHealthSchedulerLeaseAt keeps expiry-boundary tests deterministic.
func (s *Store) acquireTenantHealthSchedulerLeaseAt(ctx context.Context, ownerID string, checkedAt time.Time, ttl time.Duration) (bool, int64, error) {
	return s.acquireTenantHealthSchedulerLeaseWithClock(ctx, ownerID, func() time.Time { return checkedAt }, ttl)
}

// The clock is sampled only after the transaction has acquired SQLite's write lock.
func (s *Store) acquireTenantHealthSchedulerLeaseWithClock(ctx context.Context, ownerID string, clock func() time.Time, ttl time.Duration) (bool, int64, error) {
	if strings.TrimSpace(ownerID) == "" || len(ownerID) > 128 || clock == nil || ttl < 5*time.Second || ttl > 5*time.Minute {
		return false, 0, ErrTenantHealthSchedulerLeaseInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil { return false, 0, fmt.Errorf("begin tenant health scheduler lease acquisition: %w", err) }
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE admin_tenant_health_scheduler_lease SET updated_at = updated_at WHERE id = 1`); err != nil {
		return false, 0, fmt.Errorf("lock tenant health scheduler lease for acquisition: %w", err)
	}
	checkedAt := clock().UTC()
	result, err := tx.ExecContext(ctx, `UPDATE admin_tenant_health_scheduler_lease
		SET generation = generation + CASE WHEN owner_id = ? AND lease_until > ? THEN 0 ELSE 1 END,
		    owner_id = ?, lease_until = ?, updated_at = ?
		WHERE id = 1 AND (owner_id = ? OR lease_until <= ? OR owner_id = '')`,
		ownerID, checkedAt.UnixNano(), ownerID, checkedAt.Add(ttl).UnixNano(), formatTenantHealthTime(checkedAt),
		ownerID, checkedAt.UnixNano())
	if err != nil { return false, 0, fmt.Errorf("acquire tenant health scheduler lease: %w", err) }
	affected, err := result.RowsAffected()
	if err != nil { return false, 0, fmt.Errorf("read tenant health scheduler lease result: %w", err) }
	if affected == 0 { return false, 0, nil }
	var generation int64
	if err := tx.QueryRowContext(ctx, "SELECT generation FROM admin_tenant_health_scheduler_lease WHERE id = 1 AND owner_id = ?", ownerID).Scan(&generation); err != nil {
		return false, 0, fmt.Errorf("read acquired tenant health scheduler lease generation: %w", err)
	}
	if err := tx.Commit(); err != nil { return false, 0, fmt.Errorf("commit tenant health scheduler lease acquisition: %w", err) }
	return true, generation, nil
}

// RenewTenantHealthSchedulerLease extends only a currently valid lease owned by ownerID.
// The caller timestamp is validated for compatibility, but lease validity and the
// new deadline use the time observed immediately before the database statement.
func (s *Store) RenewTenantHealthSchedulerLease(ctx context.Context, ownerID string, now time.Time, ttl time.Duration) (bool, error) {
	if strings.TrimSpace(ownerID) == "" || len(ownerID) > 128 || now.IsZero() || ttl < 5*time.Second || ttl > 5*time.Minute {
		return false, ErrTenantHealthSchedulerLeaseInvalid
	}
	// Kept for compatibility. New scheduler code must include the fencing generation.
	return s.renewTenantHealthSchedulerLeaseWithClock(ctx, ownerID, 0, time.Now, ttl)
}

// RenewTenantHealthSchedulerLeaseWithGeneration renews only the exact lease
// generation acquired by this scheduler instance.
func (s *Store) RenewTenantHealthSchedulerLeaseWithGeneration(ctx context.Context, ownerID string, generation int64, now time.Time, ttl time.Duration) (bool, error) {
	if strings.TrimSpace(ownerID) == "" || len(ownerID) > 128 || generation <= 0 || now.IsZero() || ttl < 5*time.Second || ttl > 5*time.Minute {
		return false, ErrTenantHealthSchedulerLeaseInvalid
	}
	return s.renewTenantHealthSchedulerLeaseWithClock(ctx, ownerID, generation, time.Now, ttl)
}

// renewTenantHealthSchedulerLeaseAt preserves deterministic expiry-boundary tests.
func (s *Store) renewTenantHealthSchedulerLeaseAt(ctx context.Context, ownerID string, checkedAt time.Time, ttl time.Duration) (bool, error) {
	return s.renewTenantHealthSchedulerLeaseWithClock(ctx, ownerID, 0, func() time.Time { return checkedAt }, ttl)
}

func (s *Store) renewTenantHealthSchedulerLeaseWithGenerationAt(ctx context.Context, ownerID string, generation int64, checkedAt time.Time, ttl time.Duration) (bool, error) {
	return s.renewTenantHealthSchedulerLeaseWithClock(ctx, ownerID, generation, func() time.Time { return checkedAt }, ttl)
}

func (s *Store) renewTenantHealthSchedulerLeaseWithClock(ctx context.Context, ownerID string, generation int64, clock func() time.Time, ttl time.Duration) (bool, error) {
	if strings.TrimSpace(ownerID) == "" || len(ownerID) > 128 || generation < 0 || clock == nil || ttl < 5*time.Second || ttl > 5*time.Minute {
		return false, ErrTenantHealthSchedulerLeaseInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil { return false, fmt.Errorf("begin tenant health scheduler lease renewal: %w", err) }
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE admin_tenant_health_scheduler_lease SET updated_at = updated_at WHERE id = 1`); err != nil {
		return false, fmt.Errorf("lock tenant health scheduler lease for renewal: %w", err)
	}
	checkedAt := clock().UTC()
	query := `UPDATE admin_tenant_health_scheduler_lease
		SET lease_until = ?, updated_at = ?
		WHERE id = 1 AND owner_id = ? AND lease_until > ?`
	args := []any{checkedAt.Add(ttl).UnixNano(), formatTenantHealthTime(checkedAt), ownerID, checkedAt.UnixNano()}
	if generation > 0 {
		query += " AND generation = ?"
		args = append(args, generation)
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil { return false, fmt.Errorf("renew tenant health scheduler lease: %w", err) }
	affected, err := result.RowsAffected()
	if err != nil { return false, fmt.Errorf("read renewed tenant health scheduler lease result: %w", err) }
	if err := tx.Commit(); err != nil { return false, fmt.Errorf("commit tenant health scheduler lease renewal: %w", err) }
	return affected == 1, nil
}

// ReleaseTenantHealthSchedulerLease releases a lease by owner for compatibility.
// New scheduler code must use ReleaseTenantHealthSchedulerLeaseWithGeneration.
func (s *Store) ReleaseTenantHealthSchedulerLease(ctx context.Context, ownerID string) error {
	if strings.TrimSpace(ownerID) == "" || len(ownerID) > 128 { return ErrTenantHealthSchedulerLeaseInvalid }
	_, err := s.db.ExecContext(ctx, `UPDATE admin_tenant_health_scheduler_lease
		SET owner_id = '', lease_until = 0, updated_at = ?
		WHERE id = 1 AND owner_id = ?`, formatTenantHealthTime(time.Now().UTC()), ownerID)
	if err != nil { return fmt.Errorf("release tenant health scheduler lease: %w", err) }
	return nil
}

// ReleaseTenantHealthSchedulerLeaseWithGeneration releases only the exact lease
// generation acquired by this scheduler instance. Stale owners cannot release
// a replacement lease, even if an owner identifier is reused.
func (s *Store) ReleaseTenantHealthSchedulerLeaseWithGeneration(ctx context.Context, ownerID string, generation int64) error {
	if strings.TrimSpace(ownerID) == "" || len(ownerID) > 128 || generation <= 0 {
		return ErrTenantHealthSchedulerLeaseInvalid
	}
	_, err := s.db.ExecContext(ctx, `UPDATE admin_tenant_health_scheduler_lease
		SET owner_id = '', lease_until = 0, updated_at = ?
		WHERE id = 1 AND owner_id = ? AND generation = ?`,
		formatTenantHealthTime(time.Now().UTC()), ownerID, generation)
	if err != nil { return fmt.Errorf("release tenant health scheduler lease with generation: %w", err) }
	return nil
}

type TenantHealthSchedulerLeaseInfo struct {
	Status string
	LeaseUntil *time.Time
	Generation int64
}

// GetTenantHealthSchedulerLeaseInfo returns a redacted view of the shared
// scheduler lease. It never exposes the random owner identifier.
func (s *Store) GetTenantHealthSchedulerLeaseInfo(ctx context.Context, ownerID string, now time.Time) (TenantHealthSchedulerLeaseInfo, error) {
	if now.IsZero() { return TenantHealthSchedulerLeaseInfo{}, ErrTenantHealthSchedulerLeaseInvalid }
	var currentOwner string
	var leaseUntil int64
	var generation int64
	err := s.db.QueryRowContext(ctx, `SELECT owner_id, lease_until, generation
		FROM admin_tenant_health_scheduler_lease WHERE id = 1`).Scan(&currentOwner, &leaseUntil, &generation)
	if err != nil { return TenantHealthSchedulerLeaseInfo{}, fmt.Errorf("read tenant health scheduler lease info: %w", err) }
	info := TenantHealthSchedulerLeaseInfo{Status:"expired_or_unclaimed", Generation:generation}
	if leaseUntil > now.UTC().UnixNano() && currentOwner != "" {
		expiresAt := time.Unix(0, leaseUntil).UTC()
		info.LeaseUntil = &expiresAt
		if ownerID != "" && currentOwner == ownerID {
			info.Status = "held_by_this_instance"
		} else {
			info.Status = "held_by_other_instance"
		}
	}
	return info, nil
}
