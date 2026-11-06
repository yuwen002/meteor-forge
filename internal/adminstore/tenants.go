package adminstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const tenantActiveMigrationConstraint = "tenant is used by an active migration task"

var (
	ErrTenantNotFound = errors.New("tenant not found")
	ErrTenantConflict = errors.New("tenant code already exists")
	ErrTenantInvalidInput = errors.New("invalid tenant input")
	ErrDataSourceHasTenants = errors.New("data source is assigned to tenants")
	ErrTenantActiveMigration = errors.New("tenant is used by an active migration task")
	ErrTenantMigrationUnavailable = errors.New("tenant is unavailable for migration")
)

// Tenant is a logical tenant mapped to a registered target data source.
// It intentionally exposes no DSN or connection credentials.
type Tenant struct {
	ID string `json:"id"`
	Name string `json:"name"`
	Code string `json:"code"`
	DataSourceID string `json:"data_source_id"`
	DataSourceName string `json:"data_source_name"`
	Driver string `json:"driver"`
	Status string `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TenantInput is accepted by tenant management endpoints.
type TenantInput struct {
	Name string `json:"name"`
	Code string `json:"code"`
	DataSourceID string `json:"data_source_id"`
	Status string `json:"status"`
}

func validateTenantInput(input TenantInput) (TenantInput, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Code = strings.ToLower(strings.TrimSpace(input.Code))
	input.DataSourceID = strings.TrimSpace(input.DataSourceID)
	input.Status = strings.ToLower(strings.TrimSpace(input.Status))
	if input.Name == "" || len(input.Name) > 128 {
		return TenantInput{}, fmt.Errorf("%w: name must contain between 1 and 128 characters", ErrTenantInvalidInput)
	}
	if input.Code == "" || len(input.Code) > 64 {
		return TenantInput{}, fmt.Errorf("%w: code must contain between 1 and 64 characters", ErrTenantInvalidInput)
	}
	for _, r := range input.Code {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return TenantInput{}, fmt.Errorf("%w: code may contain only lowercase letters, digits, hyphens and underscores", ErrTenantInvalidInput)
		}
	}
	if input.DataSourceID == "" {
		return TenantInput{}, fmt.Errorf("%w: data_source_id is required", ErrTenantInvalidInput)
	}
	if input.Status == "" {
		input.Status = "active"
	}
	if input.Status != "active" && input.Status != "suspended" {
		return TenantInput{}, fmt.Errorf("%w: status must be active or suspended", ErrTenantInvalidInput)
	}
	return input, nil
}

// CreateTenant creates a tenant and records a local-api audit event.
func (s *Store) CreateTenant(ctx context.Context, raw TenantInput) (Tenant, error) {
	return s.CreateTenantWithAudit(ctx, raw, "local-api", "")
}

// CreateTenantWithAudit atomically creates a tenant and records its lifecycle event.
func (s *Store) CreateTenantWithAudit(ctx context.Context, raw TenantInput, actor, requestSource string) (Tenant, error) {
	input, err := validateTenantInput(raw)
	if err != nil { return Tenant{}, err }
	id, err := newID()
	if err != nil { return Tenant{}, err }
	now := time.Now().UTC()
	actor = tenantAuditActor(actor)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil { return Tenant{}, fmt.Errorf("begin tenant creation: %w", err) }
	defer tx.Rollback() //nolint:errcheck

	var sourceID string
	if err := tx.QueryRowContext(ctx, "SELECT id FROM admin_data_sources WHERE id = ?", input.DataSourceID).Scan(&sourceID); errors.Is(err, sql.ErrNoRows) {
		return Tenant{}, ErrNotFound
	} else if err != nil {
		return Tenant{}, fmt.Errorf("verify tenant data source: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO admin_tenants
		(id, name, code, data_source_id, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, input.Name, input.Code, input.DataSourceID, input.Status,
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		message := strings.ToLower(err.Error())
		if strings.Contains(message, "unique") { return Tenant{}, ErrTenantConflict }
		if strings.Contains(message, tenantActiveMigrationConstraint) { return Tenant{}, ErrTenantActiveMigration }
		return Tenant{}, fmt.Errorf("create tenant: %w", err)
	}
	after := TenantAuditSnapshot(input)
	if err := insertTenantAuditEvent(ctx, tx, id, "created", actor, requestSource, nil, &after, now); err != nil {
		return Tenant{}, err
	}
	if err := tx.Commit(); err != nil { return Tenant{}, fmt.Errorf("commit tenant creation: %w", err) }
	return s.GetTenant(ctx, id)
}

// ListTenants returns tenant metadata with safe data source labels.
func (s *Store) ListTenants(ctx context.Context) ([]Tenant, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT t.id, t.name, t.code, t.data_source_id,
		d.name, d.driver, t.status, t.created_at, t.updated_at
		FROM admin_tenants t JOIN admin_data_sources d ON d.id = t.data_source_id
		ORDER BY t.name COLLATE NOCASE, t.code`)
	if err != nil { return nil, fmt.Errorf("list tenants: %w", err) }
	defer rows.Close()
	items := make([]Tenant, 0)
	for rows.Next() {
		item, err := scanTenant(rows)
		if err != nil { return nil, err }
		items = append(items, item)
	}
	if err := rows.Err(); err != nil { return nil, fmt.Errorf("iterate tenants: %w", err) }
	return items, nil
}

// GetTenant returns one tenant and its registered data source metadata.
func (s *Store) GetTenant(ctx context.Context, id string) (Tenant, error) {
	row := s.db.QueryRowContext(ctx, `SELECT t.id, t.name, t.code, t.data_source_id,
		d.name, d.driver, t.status, t.created_at, t.updated_at
		FROM admin_tenants t JOIN admin_data_sources d ON d.id = t.data_source_id
		WHERE t.id = ?`, id)
	item, err := scanTenant(row)
	if errors.Is(err, sql.ErrNoRows) { return Tenant{}, ErrTenantNotFound }
	if err != nil { return Tenant{}, fmt.Errorf("get tenant: %w", err) }
	return item, nil
}

type tenantScanner interface {
	Scan(dest ...any) error
}

func scanTenant(row tenantScanner) (Tenant, error) {
	var item Tenant
	var created, updated string
	err := row.Scan(&item.ID, &item.Name, &item.Code, &item.DataSourceID,
		&item.DataSourceName, &item.Driver, &item.Status, &created, &updated)
	if err != nil { return Tenant{}, err }
	item.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil { return Tenant{}, fmt.Errorf("parse tenant creation time: %w", err) }
	item.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	if err != nil { return Tenant{}, fmt.Errorf("parse tenant update time: %w", err) }
	return item, nil
}

// UpdateTenant changes tenant metadata and records a local-api audit event.
func (s *Store) UpdateTenant(ctx context.Context, id string, raw TenantInput) (Tenant, error) {
	return s.UpdateTenantWithAudit(ctx, id, raw, "local-api", "")
}

// UpdateTenantWithAudit atomically updates tenant metadata and records before/after snapshots.
func (s *Store) UpdateTenantWithAudit(ctx context.Context, id string, raw TenantInput, actor, requestSource string) (Tenant, error) {
	input, err := validateTenantInput(raw)
	if err != nil { return Tenant{}, err }
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil { return Tenant{}, fmt.Errorf("begin tenant update: %w", err) }
	defer tx.Rollback() //nolint:errcheck
	actor = tenantAuditActor(actor)
	var before TenantAuditSnapshot
	if err := tx.QueryRowContext(ctx, "SELECT name, code, data_source_id, status FROM admin_tenants WHERE id = ?", id).
		Scan(&before.Name, &before.Code, &before.DataSourceID, &before.Status); errors.Is(err, sql.ErrNoRows) {
		return Tenant{}, ErrTenantNotFound
	} else if err != nil {
		return Tenant{}, fmt.Errorf("load tenant before update: %w", err)
	}

	var sourceID string
	if err := tx.QueryRowContext(ctx, "SELECT id FROM admin_data_sources WHERE id = ?", input.DataSourceID).Scan(&sourceID); errors.Is(err, sql.ErrNoRows) {
		return Tenant{}, ErrNotFound
	} else if err != nil {
		return Tenant{}, fmt.Errorf("verify tenant data source: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE admin_tenants SET name = ?, code = ?,
		data_source_id = ?, status = ?, updated_at = ? WHERE id = ?`,
		input.Name, input.Code, input.DataSourceID, input.Status, time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		message := strings.ToLower(err.Error())
		if strings.Contains(message, "unique") { return Tenant{}, ErrTenantConflict }
		if strings.Contains(message, tenantActiveMigrationConstraint) { return Tenant{}, ErrTenantActiveMigration }
		return Tenant{}, fmt.Errorf("update tenant: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil { return Tenant{}, err }
	if affected == 0 { return Tenant{}, ErrTenantNotFound }
	after := TenantAuditSnapshot(input)
	if err := insertTenantAuditEvent(ctx, tx, id, "updated", actor, requestSource, &before, &after, time.Now().UTC()); err != nil {
		return Tenant{}, err
	}
	if err := tx.Commit(); err != nil { return Tenant{}, fmt.Errorf("commit tenant update: %w", err) }
	return s.GetTenant(ctx, id)
}

// DeleteTenant removes only the tenant registry entry and records a local-api audit event.
func (s *Store) DeleteTenant(ctx context.Context, id string) error {
	return s.DeleteTenantWithAudit(ctx, id, "local-api", "")
}

// DeleteTenantWithAudit atomically deletes a tenant and records its final snapshot.
func (s *Store) DeleteTenantWithAudit(ctx context.Context, id, actor, requestSource string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil { return fmt.Errorf("begin tenant deletion: %w", err) }
	defer tx.Rollback() //nolint:errcheck
	actor = tenantAuditActor(actor)
	var before TenantAuditSnapshot
	if err := tx.QueryRowContext(ctx, "SELECT name, code, data_source_id, status FROM admin_tenants WHERE id = ?", id).
		Scan(&before.Name, &before.Code, &before.DataSourceID, &before.Status); errors.Is(err, sql.ErrNoRows) {
		return ErrTenantNotFound
	} else if err != nil {
		return fmt.Errorf("load tenant before deletion: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM admin_tenants WHERE id = ?", id); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), tenantActiveMigrationConstraint) { return ErrTenantActiveMigration }
		return fmt.Errorf("delete tenant: %w", err)
	}
	if err := insertTenantAuditEvent(ctx, tx, id, "deleted", actor, requestSource, &before, nil, time.Now().UTC()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil { return fmt.Errorf("commit tenant deletion: %w", err) }
	return nil
}

// TenantAuditSnapshot contains only non-secret tenant registry metadata.
type TenantAuditSnapshot struct {
	Name         string `json:"name"`
	Code         string `json:"code"`
	DataSourceID string `json:"data_source_id"`
	Status       string `json:"status"`
}

// TenantAuditEvent records a tenant lifecycle mutation with before/after metadata.
type TenantAuditEvent struct {
	ID            int64                `json:"id"`
	TenantID      string               `json:"tenant_id"`
	Action        string               `json:"action"`
	Actor         string               `json:"actor"`
	RequestSource string               `json:"request_source,omitempty"`
	Before        *TenantAuditSnapshot `json:"before,omitempty"`
	After         *TenantAuditSnapshot `json:"after,omitempty"`
	OccurredAt    time.Time            `json:"occurred_at"`
}

func tenantAuditActor(actor string) string {
	if strings.TrimSpace(actor) == "" { return "local-api" }
	return strings.TrimSpace(actor)
}

func insertTenantAuditEvent(ctx context.Context, tx *sql.Tx, tenantID, action, actor, requestSource string, before, after *TenantAuditSnapshot, occurred time.Time) error {
	beforeJSON, err := json.Marshal(before)
	if err != nil { return fmt.Errorf("encode tenant audit before snapshot: %w", err) }
	afterJSON, err := json.Marshal(after)
	if err != nil { return fmt.Errorf("encode tenant audit after snapshot: %w", err) }
	_, err = tx.ExecContext(ctx, `INSERT INTO admin_tenant_audit_events
		(tenant_id, action, actor, request_source, before_json, after_json, occurred_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		tenantID, action, tenantAuditActor(actor), requestSource, string(beforeJSON), string(afterJSON), occurred.UTC().Format(time.RFC3339Nano))
	if err != nil { return fmt.Errorf("record tenant lifecycle audit event: %w", err) }
	return nil
}

// ListTenantAuditEvents returns lifecycle events for a tenant ID, including after deletion.
func (s *Store) ListTenantAuditEvents(ctx context.Context, tenantID string, limit, offset int) ([]TenantAuditEvent, int, error) {
	if limit <= 0 || limit > 200 { limit = 50 }
	if offset < 0 { offset = 0 }
	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM admin_tenant_audit_events WHERE tenant_id = ?", tenantID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count tenant audit events: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, tenant_id, action, actor, request_source, before_json, after_json, occurred_at
		FROM admin_tenant_audit_events WHERE tenant_id = ? ORDER BY id DESC LIMIT ? OFFSET ?`, tenantID, limit, offset)
	if err != nil { return nil, 0, fmt.Errorf("list tenant audit events: %w", err) }
	defer rows.Close()
	items := make([]TenantAuditEvent, 0)
	for rows.Next() {
		var item TenantAuditEvent
		var beforeJSON, afterJSON, occurred string
		if err := rows.Scan(&item.ID, &item.TenantID, &item.Action, &item.Actor, &item.RequestSource, &beforeJSON, &afterJSON, &occurred); err != nil {
			return nil, 0, fmt.Errorf("scan tenant audit event: %w", err)
		}
		if err := json.Unmarshal([]byte(beforeJSON), &item.Before); err != nil { return nil, 0, fmt.Errorf("decode tenant audit before snapshot: %w", err) }
		if err := json.Unmarshal([]byte(afterJSON), &item.After); err != nil { return nil, 0, fmt.Errorf("decode tenant audit after snapshot: %w", err) }
		item.OccurredAt, err = time.Parse(time.RFC3339Nano, occurred)
		if err != nil { return nil, 0, fmt.Errorf("parse tenant audit timestamp: %w", err) }
		items = append(items, item)
	}
	if err := rows.Err(); err != nil { return nil, 0, fmt.Errorf("read tenant audit events: %w", err) }
	return items, total, nil
}
