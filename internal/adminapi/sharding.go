package adminapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/yuwen002/meteor-forge/internal/adminstore"
	"github.com/yuwen002/meteor-forge/pkg/sharding"
)

type shardRuleStore interface {
	CreateShardRule(context.Context, adminstore.ShardRuleInput) (adminstore.ShardRule, error)
	ListShardRules(context.Context) ([]adminstore.ShardRule, error)
	GetShardRule(context.Context, string) (adminstore.ShardRule, error)
	UpdateShardRule(context.Context, string, adminstore.ShardRuleInput) (adminstore.ShardRule, error)
	DeleteShardRule(context.Context, string) error
}

type shardRoutePreviewRequest struct {
	Timestamp string `json:"timestamp,omitempty"`
	ID        *int64 `json:"id,omitempty"`
	StartTime string `json:"start_time,omitempty"`
	EndTime   string `json:"end_time,omitempty"`
	StartID   *int64 `json:"start_id,omitempty"`
	EndID     *int64 `json:"end_id,omitempty"`
}

func (s *Server) registerShardingRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET "+apiPrefix+"/shard-rules", s.listShardRules)
	mux.HandleFunc("POST "+apiPrefix+"/shard-rules", s.createShardRule)
	mux.HandleFunc("GET "+apiPrefix+"/shard-rules/{id}", s.getShardRule)
	mux.HandleFunc("PUT "+apiPrefix+"/shard-rules/{id}", s.updateShardRule)
	mux.HandleFunc("DELETE "+apiPrefix+"/shard-rules/{id}", s.deleteShardRule)
	mux.HandleFunc("POST "+apiPrefix+"/shard-rules/{id}/preview", s.previewShardRoute)
}

func (s *Server) requireShardStore(w http.ResponseWriter) bool {
	if s.shards != nil { return true }
	writeAPIError(w, http.StatusServiceUnavailable, "metadata_unavailable", "management metadata store is not configured")
	return false
}

func (s *Server) listShardRules(w http.ResponseWriter, r *http.Request) {
	if !s.requireShardStore(w) { return }
	items, err := s.shards.ListShardRules(r.Context())
	if err != nil { writeAPIError(w, http.StatusInternalServerError, "internal_error", "unable to list shard rules"); return }
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) createShardRule(w http.ResponseWriter, r *http.Request) {
	if !s.requireShardStore(w) { return }
	var input adminstore.ShardRuleInput
	if err := decodeJSON(w, r, &input); err != nil { writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error()); return }
	item, err := s.shards.CreateShardRule(r.Context(), input)
	if err != nil { writeShardRuleError(w, err); return }
	writeJSON(w, http.StatusCreated, item)
}

func (s *Server) getShardRule(w http.ResponseWriter, r *http.Request) {
	if !s.requireShardStore(w) { return }
	item, err := s.shards.GetShardRule(r.Context(), r.PathValue("id"))
	if err != nil { writeShardRuleError(w, err); return }
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) updateShardRule(w http.ResponseWriter, r *http.Request) {
	if !s.requireShardStore(w) { return }
	var input adminstore.ShardRuleInput
	if err := decodeJSON(w, r, &input); err != nil { writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error()); return }
	item, err := s.shards.UpdateShardRule(r.Context(), r.PathValue("id"), input)
	if err != nil { writeShardRuleError(w, err); return }
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) deleteShardRule(w http.ResponseWriter, r *http.Request) {
	if !s.requireShardStore(w) { return }
	if err := s.shards.DeleteShardRule(r.Context(), r.PathValue("id")); err != nil { writeShardRuleError(w, err); return }
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) previewShardRoute(w http.ResponseWriter, r *http.Request) {
	if !s.requireShardStore(w) { return }
	rule, err := s.shards.GetShardRule(r.Context(), r.PathValue("id"))
	if err != nil { writeShardRuleError(w, err); return }
	var input shardRoutePreviewRequest
	if err := decodeJSON(w, r, &input); err != nil { writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error()); return }

	options := make([]sharding.Option, 0, 1)
	switch rule.ShardType {
	case "id_hash":
		options = append(options, sharding.WithHashShards(rule.HashShards))
	case "id_range":
		options = append(options, sharding.WithRangeSize(rule.RangeSize))
	}
	coreRule := sharding.NewShardRule(sharding.ShardType(rule.ShardType), rule.BaseTable, options...)
	var tables []string

	switch rule.ShardType {
	case "year", "quarter", "month", "week", "ten_day":
		if input.Timestamp != "" {
			ts, parseErr := time.Parse(time.RFC3339Nano, input.Timestamp)
			if parseErr != nil { writeAPIError(w, http.StatusBadRequest, "invalid_time", "timestamp must be RFC3339"); return }
			table, routeErr := coreRule.GetTableNameByTime(ts)
			if routeErr != nil { writeAPIError(w, http.StatusBadRequest, "invalid_route", "unable to calculate shard route"); return }
			tables = []string{table}
		} else if input.StartTime != "" && input.EndTime != "" {
			start, startErr := time.Parse(time.RFC3339Nano, input.StartTime)
			end, endErr := time.Parse(time.RFC3339Nano, input.EndTime)
			if startErr != nil || endErr != nil || start.After(end) {
				writeAPIError(w, http.StatusBadRequest, "invalid_time_range", "start_time and end_time must be valid RFC3339 values with start_time not after end_time")
				return
			}
			if end.Sub(start) > 3660*24*time.Hour {
				writeAPIError(w, http.StatusBadRequest, "range_too_large", "time range preview is limited to ten years")
				return
			}
			tables, err = coreRule.GetTableNamesByTimeRange(sharding.TimeRange{Start: start, End: end})
		} else {
			writeAPIError(w, http.StatusBadRequest, "missing_route_input", "provide timestamp or both start_time and end_time")
			return
		}
	case "id_hash", "id_range":
		if input.ID != nil {
			if *input.ID < 0 { writeAPIError(w, http.StatusBadRequest, "invalid_id", "id must not be negative"); return }
			table, routeErr := coreRule.GetTableNameByID(*input.ID)
			if routeErr != nil { writeAPIError(w, http.StatusBadRequest, "invalid_route", "unable to calculate shard route"); return }
			tables = []string{table}
		} else if input.StartID != nil && input.EndID != nil {
			if *input.StartID < 0 || *input.EndID < 0 || *input.StartID > *input.EndID {
				writeAPIError(w, http.StatusBadRequest, "invalid_id_range", "start_id and end_id must be non-negative and start_id must not exceed end_id")
				return
			}
			if rule.ShardType == "id_range" && (*input.EndID/rule.RangeSize-*input.StartID/rule.RangeSize) >= 1000 {
				writeAPIError(w, http.StatusBadRequest, "range_too_large", "ID range preview is limited to 1000 physical tables")
				return
			}
			tables, err = coreRule.GetTableNamesByIDRange(sharding.IDRange{Start: *input.StartID, End: *input.EndID})
		} else {
			writeAPIError(w, http.StatusBadRequest, "missing_route_input", "provide id or both start_id and end_id")
			return
		}
	default:
		writeAPIError(w, http.StatusInternalServerError, "invalid_shard_rule", "stored shard rule uses an unsupported strategy")
		return
	}
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_route", "unable to calculate shard route")
		return
	}
	if len(tables) > 4096 {
		writeAPIError(w, http.StatusBadRequest, "range_too_large", "route preview exceeds 4096 physical tables")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"shard_rule_id": rule.ID,
		"data_source_id": rule.DataSourceID,
		"base_table": rule.BaseTable,
		"shard_type": rule.ShardType,
		"tables": tables,
		"table_count": len(tables),
	})
}

func writeShardRuleError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, adminstore.ErrShardRuleNotFound):
		writeAPIError(w, http.StatusNotFound, "shard_rule_not_found", "shard rule not found")
	case errors.Is(err, adminstore.ErrShardRuleConflict):
		writeAPIError(w, http.StatusConflict, "shard_rule_conflict", "a shard rule already exists for this data source and base table")
	case errors.Is(err, adminstore.ErrShardRuleDataSourceNotFound):
		writeAPIError(w, http.StatusNotFound, "data_source_not_found", "referenced data source not found")
	case errors.Is(err, adminstore.ErrShardRuleInvalidInput):
		message := err.Error()
		if index := strings.Index(message, ": "); index >= 0 { message = message[index+2:] }
		writeAPIError(w, http.StatusBadRequest, "invalid_shard_rule", message)
	default:
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "unable to process shard rule request")
	}
}
