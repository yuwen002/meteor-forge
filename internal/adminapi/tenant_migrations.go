package adminapi

import (
	"errors"
	"net"
	"net/http"

	"github.com/yuwen002/meteor-forge/internal/adminstore"
)

// createTenantMigrationTask submits the same core migration operation as the
// data-source endpoint, while recording the tenant that requested the task.
// Execution remains serialized by data source because tenants may share one DB.
func (s *Server) createTenantMigrationTask(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) || s.tasks == nil {
		return
	}
	if !s.requireMigrationDirectory(w) {
		return
	}
	if s.tenants == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "tenant_store_unavailable", "tenant metadata is unavailable")
		return
	}

	tenant, err := s.tenants.GetTenant(r.Context(), r.PathValue("id"))
	if err != nil {
		writeTenantStoreError(w, err)
		return
	}
	if tenant.Status != "active" {
		writeAPIError(w, http.StatusConflict, "tenant_suspended", "migration tasks cannot be submitted for a suspended tenant")
		return
	}

	var request createMigrationTaskRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	switch request.Action {
	case "up":
	case "down", "down_steps", "to_version":
		if !request.Confirm {
			writeAPIError(w, http.StatusBadRequest, "confirmation_required", "rollback requires explicit confirmation")
			return
		}
		if request.Action == "down_steps" && (request.Steps < 1 || request.Steps > 100) {
			writeAPIError(w, http.StatusBadRequest, "invalid_steps", "steps must be between 1 and 100")
			return
		}
		if request.Action == "to_version" && request.TargetVersion == "" {
			writeAPIError(w, http.StatusBadRequest, "invalid_version", "target_version is required; use 0 to roll back all migrations")
			return
		}
	default:
		writeAPIError(w, http.StatusBadRequest, "invalid_action", "action must be up, down, down_steps, or to_version")
		return
	}

	requestSource, _, _ := net.SplitHostPort(r.RemoteAddr)
	if requestSource == "" {
		requestSource = r.RemoteAddr
	}
	task, err := s.tasks.Submit(r.Context(), adminstore.MigrationTask{
		TenantID: tenant.ID, DataSourceID: tenant.DataSourceID,
		Action: request.Action, TargetVersion: request.TargetVersion,
		Steps: request.Steps, RequestSource: requestSource,
	})
	if err != nil {
		if errors.Is(err, adminstore.ErrTenantNotFound) || errors.Is(err, adminstore.ErrTenantMigrationUnavailable) {
			writeAPIError(w, http.StatusConflict, "tenant_migration_unavailable", "tenant mapping or status changed before the migration task was queued")
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "task_submission_failed", "unable to create tenant migration task")
		return
	}
	writeJSON(w, http.StatusAccepted, task)
}
