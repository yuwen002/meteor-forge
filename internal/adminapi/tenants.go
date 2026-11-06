package adminapi

import (
	"context"
	"errors"
	"net/http"
	"net"
	"strconv"
	"time"

	"github.com/yuwen002/meteor-forge/internal/adminstore"
)

type tenantStore interface {
	CreateTenant(context.Context, adminstore.TenantInput) (adminstore.Tenant, error)
	CreateTenantWithAudit(context.Context, adminstore.TenantInput, string, string) (adminstore.Tenant, error)
	ListTenants(context.Context) ([]adminstore.Tenant, error)
	GetTenant(context.Context, string) (adminstore.Tenant, error)
	UpdateTenant(context.Context, string, adminstore.TenantInput) (adminstore.Tenant, error)
	UpdateTenantWithAudit(context.Context, string, adminstore.TenantInput, string, string) (adminstore.Tenant, error)
	DeleteTenant(context.Context, string) error
	DeleteTenantWithAudit(context.Context, string, string, string) error
	ListTenantAuditEvents(context.Context, string, int, int) ([]adminstore.TenantAuditEvent, int, error)
}

func (s *Server) registerTenantRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET "+apiPrefix+"/tenants", s.listTenants)
	mux.HandleFunc("POST "+apiPrefix+"/tenants", s.createTenant)
	mux.HandleFunc("GET "+apiPrefix+"/tenants/{id}", s.getTenant)
	mux.HandleFunc("PUT "+apiPrefix+"/tenants/{id}", s.updateTenant)
	mux.HandleFunc("DELETE "+apiPrefix+"/tenants/{id}", s.deleteTenant)
	mux.HandleFunc("GET "+apiPrefix+"/tenants/{id}/audit", s.listTenantAuditEvents)
	mux.HandleFunc("POST "+apiPrefix+"/tenants/{id}/test", s.testTenantConnection)
}

func (s *Server) requireTenantStore(w http.ResponseWriter) bool {
	if s.tenants != nil { return true }
	writeAPIError(w, http.StatusServiceUnavailable, "metadata_unavailable", "management metadata store is not configured")
	return false
}

func (s *Server) listTenants(w http.ResponseWriter, r *http.Request) {
	if !s.requireTenantStore(w) { return }
	items, err := s.tenants.ListTenants(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "unable to list tenants")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) createTenant(w http.ResponseWriter, r *http.Request) {
	if !s.requireTenantStore(w) { return }
	var input adminstore.TenantInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	item, err := s.tenants.CreateTenantWithAudit(r.Context(), input, authenticatedActor(r), tenantRequestSource(r))
	if err != nil { writeTenantStoreError(w, err); return }
	writeJSON(w, http.StatusCreated, item)
}

func (s *Server) getTenant(w http.ResponseWriter, r *http.Request) {
	if !s.requireTenantStore(w) { return }
	item, err := s.tenants.GetTenant(r.Context(), r.PathValue("id"))
	if err != nil { writeTenantStoreError(w, err); return }
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) updateTenant(w http.ResponseWriter, r *http.Request) {
	if !s.requireTenantStore(w) { return }
	var input adminstore.TenantInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	item, err := s.tenants.UpdateTenantWithAudit(r.Context(), r.PathValue("id"), input, authenticatedActor(r), tenantRequestSource(r))
	if err != nil { writeTenantStoreError(w, err); return }
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) deleteTenant(w http.ResponseWriter, r *http.Request) {
	if !s.requireTenantStore(w) { return }
	if err := s.tenants.DeleteTenantWithAudit(r.Context(), r.PathValue("id"), authenticatedActor(r), tenantRequestSource(r)); err != nil {
		writeTenantStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listTenantAuditEvents(w http.ResponseWriter, r *http.Request) {
	if !s.requireTenantStore(w) {
		return
	}
	limit, offset := 50, 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 200 {
			writeAPIError(w, http.StatusBadRequest, "invalid_limit", "limit must be between 1 and 200")
			return
		}
		limit = value
	}
	if raw := r.URL.Query().Get("offset"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 || value > 10000000 {
			writeAPIError(w, http.StatusBadRequest, "invalid_offset", "offset must be between 0 and 10000000")
			return
		}
		offset = value
	}
	items, total, err := s.tenants.ListTenantAuditEvents(r.Context(), r.PathValue("id"), limit, offset)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "tenant_audit_query_failed", "unable to read tenant audit history")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
}

func tenantRequestSource(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	return r.RemoteAddr
}

func (s *Server) testTenantConnection(w http.ResponseWriter, r *http.Request) {
	if !s.requireTenantStore(w) || s.store == nil { return }
	item, err := s.tenants.GetTenant(r.Context(), r.PathValue("id"))
	if err != nil { writeTenantStoreError(w, err); return }

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	db, err := s.pools.Get(ctx, item.DataSourceID, func(ctx context.Context, id string) (string, string, error) {
		driver, dsn, err := s.store.ConnectionConfig(ctx, id)
		if err != nil { return "", "", err }
		driverName := map[string]string{"mysql": "mysql", "postgres": "pgx", "sqlite": "sqlite", "clickhouse": "clickhouse"}[driver]
		if driverName == "" { return "", "", errors.New("unsupported database driver") }
		return driverName, dsn, nil
	})
	if err == nil { err = db.PingContext(ctx) }
	if err != nil {
		if db != nil { s.pools.InvalidateIfCurrent(item.DataSourceID, db) }
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "message": "connection failed; verify the tenant data source"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "tenant data source connection successful"})
}

func writeTenantStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, adminstore.ErrTenantNotFound):
		writeAPIError(w, http.StatusNotFound, "tenant_not_found", "tenant not found")
	case errors.Is(err, adminstore.ErrNotFound):
		writeAPIError(w, http.StatusNotFound, "data_source_not_found", "tenant data source not found")
	case errors.Is(err, adminstore.ErrTenantConflict):
		writeAPIError(w, http.StatusConflict, "tenant_conflict", "a tenant with this code already exists")
	case errors.Is(err, adminstore.ErrDataSourceHasTenants):
		writeAPIError(w, http.StatusConflict, "data_source_has_tenants", "the data source cannot be changed or deleted while tenants reference it")
	case errors.Is(err, adminstore.ErrTenantActiveMigration):
		writeAPIError(w, http.StatusConflict, "tenant_has_active_migration", "the tenant cannot be changed or deleted while a migration task is queued or running for its data source")
	case errors.Is(err, adminstore.ErrTenantInvalidInput):
		writeAPIError(w, http.StatusBadRequest, "invalid_tenant", err.Error())
	default:
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "unable to process tenant request")
	}
}
