package adminapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/yuwen002/meteor-forge/internal/adminstore"
)

func (s *Server) registerAuditRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET "+apiPrefix+"/audit-events", s.listOperationAuditEvents)
}

func (s *Server) listOperationAuditEvents(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) { return }
	store, ok := s.store.(*adminstore.Store)
	if !ok {
		writeAPIError(w, http.StatusServiceUnavailable, "audit_store_unavailable", "operation audit storage is unavailable")
		return
	}

	query := r.URL.Query()
	filter := adminstore.OperationAuditFilter{
		Kind: strings.TrimSpace(query.Get("kind")),
		Status: strings.TrimSpace(query.Get("status")),
		TaskID: strings.TrimSpace(query.Get("task_id")),
		DataSourceID: strings.TrimSpace(query.Get("data_source_id")),
		TenantID: strings.TrimSpace(query.Get("tenant_id")),
		ShardRuleID: strings.TrimSpace(query.Get("shard_rule_id")),
	}
	if filter.Kind != "" && filter.Kind != "migration" && filter.Kind != "shard_ddl" {
		writeAPIError(w, http.StatusBadRequest, "invalid_audit_kind", "kind must be migration or shard_ddl")
		return
	}
	switch filter.Status {
	case "", "queued", "running", "succeeded", "failed", "canceled", "interrupted":
	default:
		writeAPIError(w, http.StatusBadRequest, "invalid_audit_status", "unsupported audit status")
		return
	}
	for name, value := range map[string]string{
		"task_id":filter.TaskID, "data_source_id":filter.DataSourceID,
		"tenant_id":filter.TenantID, "shard_rule_id":filter.ShardRuleID,
	} {
		if len(value) > 128 {
			writeAPIError(w, http.StatusBadRequest, "invalid_"+name, name+" must be at most 128 characters")
			return
		}
	}

	parseTime := func(name string) (*time.Time, bool) {
		raw := strings.TrimSpace(query.Get(name))
		if raw == "" { return nil, true }
		value, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_"+name, name+" must be an RFC3339 timestamp")
			return nil, false
		}
		value = value.UTC()
		return &value, true
	}
	from, ok := parseTime("from")
	if !ok { return }
	to, ok := parseTime("to")
	if !ok { return }
	if from != nil && to != nil && from.After(*to) {
		writeAPIError(w, http.StatusBadRequest, "invalid_time_range", "from must be earlier than or equal to to")
		return
	}
	filter.From, filter.To = from, to

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

	items, total, err := store.ListOperationAuditEvents(r.Context(), limit, offset, filter)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "audit_query_failed", "unable to list operation audit events")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "total": total, "limit": limit, "offset": offset,
	})
}
