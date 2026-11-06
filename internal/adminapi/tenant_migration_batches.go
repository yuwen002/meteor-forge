package adminapi

import (
	"context"
	"net"
	"net/http"
	"strings"

	"github.com/yuwen002/meteor-forge/internal/adminstore"
)

const maxTenantMigrationBatchSize = 100

type createTenantMigrationBatchRequest struct {
	TenantIDs []string `json:"tenant_ids"`
}

type tenantMigrationBatchFailure struct {
	TenantID string `json:"tenant_id"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

type tenantMigrationBatchResponse struct {
	ID          string                         `json:"id"`
	Total       int                            `json:"total"`
	Queued      int                            `json:"queued"`
	Running     int                            `json:"running"`
	Succeeded   int                            `json:"succeeded"`
	Failed      int                            `json:"failed"`
	Canceled    int                            `json:"canceled"`
	Interrupted int                            `json:"interrupted"`
	Failures    []tenantMigrationBatchFailure   `json:"failures"`
	Tasks       []adminstore.MigrationTask      `json:"tasks"`
}

// createTenantMigrationBatch fans an Up operation out to a bounded set of
// active tenants. Each child task is durable and independently auditable;
// a failed submission does not prevent later tenants from being submitted.
func (s *Server) createTenantMigrationBatch(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) || s.tasks == nil || !s.requireMigrationDirectory(w) {
		return
	}
	if s.tenants == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "tenant_store_unavailable", "tenant metadata is unavailable")
		return
	}
	var request createTenantMigrationBatchRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if len(request.TenantIDs) == 0 || len(request.TenantIDs) > maxTenantMigrationBatchSize {
		writeAPIError(w, http.StatusBadRequest, "invalid_batch_size", "tenant_ids must contain between 1 and 100 entries")
		return
	}

	ids := make([]string, 0, len(request.TenantIDs))
	seen := make(map[string]struct{}, len(request.TenantIDs))
	for _, rawID := range request.TenantIDs {
		id := strings.TrimSpace(rawID)
		if id == "" {
			writeAPIError(w, http.StatusBadRequest, "invalid_tenant_id", "tenant_ids cannot contain empty values")
			return
		}
		if _, exists := seen[id]; exists {
			writeAPIError(w, http.StatusBadRequest, "duplicate_tenant_id", "tenant_ids must be unique")
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}

	allTenants, err := s.tenants.ListTenants(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "tenant_query_failed", "unable to load tenant metadata")
		return
	}
	byID := make(map[string]adminstore.Tenant, len(allTenants))
	for _, item := range allTenants { byID[item.ID] = item }
	selected := make([]adminstore.Tenant, 0, len(ids))
	for _, id := range ids {
		item, ok := byID[id]
		if !ok {
			writeAPIError(w, http.StatusNotFound, "tenant_not_found", "one or more selected tenants do not exist")
			return
		}
		if item.Status != "active" {
			writeAPIError(w, http.StatusConflict, "tenant_suspended", "all selected tenants must be active")
			return
		}
		selected = append(selected, item)
	}

	batchID, err := newTaskID()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "batch_id_failed", "unable to create migration batch")
		return
	}
	requestSource, _, _ := net.SplitHostPort(r.RemoteAddr)
	if requestSource == "" { requestSource = r.RemoteAddr }

	response := tenantMigrationBatchResponse{
		ID: batchID, Total: len(selected),
		Failures: make([]tenantMigrationBatchFailure, 0),
		Tasks: make([]adminstore.MigrationTask, 0, len(selected)),
	}
	for _, item := range selected {
		task, submitErr := s.tasks.Submit(r.Context(), adminstore.MigrationTask{
			BatchID: batchID, TenantID: item.ID, DataSourceID: item.DataSourceID,
			Action: "up", RequestSource: requestSource, Actor: authenticatedActor(r),
		})
		if submitErr != nil {
			response.Failed++
			response.Failures = append(response.Failures, tenantMigrationBatchFailure{
				TenantID: item.ID, Code: "submission_failed",
				Message: "unable to queue migration for this tenant; other tenants were processed independently",
			})
			continue
		}
		response.Queued++
		response.Tasks = append(response.Tasks, task)
	}
	writeJSON(w, http.StatusAccepted, response)
}

func (s *Server) getTenantMigrationBatch(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) { return }
	store, ok := s.store.(interface {
		ListMigrationTasksByBatch(context.Context, string) ([]adminstore.MigrationTask, error)
	})
	if !ok {
		writeAPIError(w, http.StatusServiceUnavailable, "batch_store_unavailable", "migration batch storage is unavailable")
		return
	}
	items, err := store.ListMigrationTasksByBatch(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "batch_query_failed", "unable to read migration batch")
		return
	}
	if len(items) == 0 {
		writeAPIError(w, http.StatusNotFound, "batch_not_found", "migration batch not found")
		return
	}
	response := tenantMigrationBatchResponse{
		ID: items[0].BatchID, Total: len(items),
		Tasks: items, Failures: make([]tenantMigrationBatchFailure, 0),
	}
	for _, item := range items {
		switch item.Status {
		case "queued": response.Queued++
		case "running": response.Running++
		case "succeeded": response.Succeeded++
		case "failed":
			response.Failed++
			response.Failures = append(response.Failures, tenantMigrationBatchFailure{
				TenantID: item.TenantID, Code: "migration_failed",
				Message: item.Message,
			})
		case "canceled":
			response.Canceled++
			response.Failures = append(response.Failures, tenantMigrationBatchFailure{
				TenantID: item.TenantID, Code: "migration_canceled",
				Message: item.Message,
			})
		case "interrupted":
			response.Interrupted++
			response.Failures = append(response.Failures, tenantMigrationBatchFailure{
				TenantID: item.TenantID, Code: "migration_interrupted",
				Message: item.Message,
			})
		}
	}
	writeJSON(w, http.StatusOK, response)
}
