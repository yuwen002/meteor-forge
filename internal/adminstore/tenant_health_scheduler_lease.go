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
	return s.acquireTenantHealthSchedulerLeaseAt(ctx, ownerID, time.Now().UTC(), ttl)
}

// acquireTenantHealthSchedulerLeaseAt anchors expiry comparison and the new
// deadline to one timestamp. It supports deterministic expiry-boundary tests.
func (s *Store) acquireTenantHealthSchedulerLeaseAt(ctx context.Context, ownerID string, checkedAt time.Time, ttl time.Duration) (bool, int64, error) {
	if strings.TrimSpace(ownerID) == "" || len(ownerID) > 128 || checkedAt.IsZero() || ttl < 5*time.Second || ttl > 5*time.Minute {
		return false, 0, ErrTenantHealthSchedulerLeaseInvalid
	}
	checkedAt = checkedAt.UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE admin_tenant_health_scheduler_lease
		SET generation = generation + CASE WHEN owner_id = ? THEN 0 ELSE 1 END,
		    owner_id = ?, lease_until = ?, updated_at = ?
		WHERE id = 1 AND (owner_id = ? OR lease_until <= ? OR owner_id = '')`,
		ownerID, ownerID, checkedAt.Add(ttl).UnixNano(), formatTenantHealthTime(checkedAt),
		ownerID, checkedAt.UnixNano())
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
// The caller timestamp is validated for compatibility, but lease validity and the
// new deadline use the time observed immediately before the database statement.
func (s *Store) RenewTenantHealthSchedulerLease(ctx context.Context, ownerID string, now time.Time, ttl time.Duration) (bool, error) {
	if strings.TrimSpace(ownerID) == "" || len(ownerID) > 128 || now.IsZero() || ttl < 5*time.Second || ttl > 5*time.Minute {
		return false, ErrTenantHealthSchedulerLeaseInvalid
	}
	return s.renewTenantHealthSchedulerLeaseAt(ctx, ownerID, time.Now().UTC(), ttl)
}

// renewTenantHealthSchedulerLeaseAt keeps the lease comparison and extension
// anchored to one timestamp. It is split out so expiry-boundary tests can use
// deterministic logical times without weakening the production wrapper.
func (s *Store) renewTenantHealthSchedulerLeaseAt(ctx context.Context, ownerID string, checkedAt time.Time, ttl time.Duration) (bool, error) {
	if strings.TrimSpace(ownerID) == "" || len(ownerID) > 128 || checkedAt.IsZero() || ttl < 5*time.Second || ttl > 5*time.Minute {
		return false, ErrTenantHealthSchedulerLeaseInvalid
	}
	checkedAt = checkedAt.UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE admin_tenant_health_scheduler_lease
		SET lease_until = ?, updated_at = ?
		WHERE id = 1 AND owner_id = ? AND lease_until > ?`,
		checkedAt.Add(ttl).UnixNano(), formatTenantHealthTime(checkedAt), ownerID, checkedAt.UnixNano())
	if err != nil { return false, fmt.Errorf("renew tenant health scheduler lease: %w", err) }
	affected, err := result.RowsAffected()
	if err != nil { return false, fmt.Errorf("read renewed tenant health scheduler lease result: %w", err) }
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
