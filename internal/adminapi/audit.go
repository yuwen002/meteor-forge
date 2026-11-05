package adminapi

import (
	"net/http"
	"strconv"

	"github.com/yuwen002/meteor-forge/internal/adminstore"
)

func (s *Server) registerAuditRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET "+apiPrefix+"/audit-events", s.listOperationAuditEvents)
}

func (s *Server) listOperationAuditEvents(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	store, ok := s.store.(*adminstore.Store)
	if !ok {
		writeAPIError(w, http.StatusServiceUnavailable, "audit_store_unavailable", "operation audit storage is unavailable")
		return
	}

	query := r.URL.Query()
	kind := query.Get("kind")
	if kind != "" && kind != "migration" && kind != "shard_ddl" {
		writeAPIError(w, http.StatusBadRequest, "invalid_audit_kind", "kind must be migration or shard_ddl")
		return
	}
	status := query.Get("status")
	switch status {
	case "", "queued", "running", "succeeded", "failed", "canceled", "interrupted":
	default:
		writeAPIError(w, http.StatusBadRequest, "invalid_audit_status", "unsupported audit status")
		return
	}

	limit := 50
	if raw := query.Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 200 {
			writeAPIError(w, http.StatusBadRequest, "invalid_limit", "limit must be between 1 and 200")
			return
		}
		limit = value
	}
	offset := 0
	if raw := query.Get("offset"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 || value > 10000000 {
			writeAPIError(w, http.StatusBadRequest, "invalid_offset", "offset must be between 0 and 10000000")
			return
		}
		offset = value
	}

	items, total, err := store.ListOperationAuditEvents(r.Context(), limit, offset, kind, status)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "audit_query_failed", "unable to list operation audit events")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "total": total, "limit": limit, "offset": offset,
	})
}
