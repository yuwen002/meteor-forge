package adminstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// CreateDiscoveredTenantsWithAudit imports a confirmed discovery batch atomically.
// It only creates local tenant registry entries; it never updates or deletes tenants.
func (s *Store) CreateDiscoveredTenantsWithAudit(ctx context.Context, raw []TenantInput, actor, requestSource string) ([]Tenant, error) {
	if len(raw) == 0 || len(raw) > 500 {
		return nil, fmt.Errorf("%w: discovery batch must contain between 1 and 500 tenants", ErrTenantInvalidInput)
	}
	inputs := make([]TenantInput, len(raw))
	ids := make([]string, len(raw))
	for i, item := range raw {
		validated, err := validateTenantInput(item)
		if err != nil { return nil, err }
		inputs[i] = validated
		for j := 0; j < i; j++ {
			if strings.EqualFold(inputs[j].Code, validated.Code) { return nil, ErrTenantConflict }
		}
		id, err := newID()
		if err != nil { return nil, err }
		ids[i] = id
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil { return nil, fmt.Errorf("begin tenant discovery import: %w", err) }
	defer tx.Rollback() //nolint:errcheck
	now := time.Now().UTC()
	for i, input := range inputs {
		var sourceID string
		if err := tx.QueryRowContext(ctx, "SELECT id FROM admin_data_sources WHERE id = ?", input.DataSourceID).Scan(&sourceID); errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		} else if err != nil {
			return nil, fmt.Errorf("verify discovered tenant data source: %w", err)
		}
		stamp := now.Format(time.RFC3339Nano)
		_, err := tx.ExecContext(ctx, `INSERT INTO admin_tenants
			(id, name, code, data_source_id, status, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			ids[i], input.Name, input.Code, input.DataSourceID, input.Status, stamp, stamp)
		if err != nil {
			message := strings.ToLower(err.Error())
			if strings.Contains(message, "unique") { return nil, ErrTenantConflict }
			if strings.Contains(message, tenantActiveMigrationConstraint) { return nil, ErrTenantActiveMigration }
			return nil, fmt.Errorf("insert discovered tenant: %w", err)
		}
		snapshot := TenantAuditSnapshot(input)
		if err := insertTenantAuditEvent(ctx, tx, ids[i], "created", actor, requestSource, nil, &snapshot, now); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil { return nil, fmt.Errorf("commit tenant discovery import: %w", err) }
	result := make([]Tenant, 0, len(ids))
	for _, id := range ids {
		item, err := s.GetTenant(ctx, id)
		if err != nil { return nil, err }
		result = append(result, item)
	}
	return result, nil
}
