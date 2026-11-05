package adminapi

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
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
	mux.HandleFunc("GET "+apiPrefix+"/shard-rules/{id}/tables", s.listPhysicalShardTables)
	mux.HandleFunc("POST "+apiPrefix+"/shard-rules/{id}/tables/exists", s.checkPhysicalShardTable)
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


type shardTableExistsRequest struct {
	TableName string `json:"table_name"`
}

func (s *Server) targetShardDB(ctx context.Context, dataSourceID string) (*sql.DB, error) {
	if s.store == nil || s.pools == nil { return nil, errors.New("target database access is unavailable") }
	return s.pools.Get(ctx, dataSourceID, func(ctx context.Context, id string) (string, string, error) {
		driver, dsn, err := s.store.ConnectionConfig(ctx, id)
		if err != nil { return "", "", err }
		driverName := map[string]string{"mysql": "mysql", "postgres": "pgx", "sqlite": "sqlite", "clickhouse": "clickhouse"}[driver]
		if driverName == "" { return "", "", errors.New("unsupported database driver") }
		return driverName, dsn, nil
	})
}

func physicalTableNames(ctx context.Context, db *sql.DB, driver string) ([]string, error) {
	var query string
	switch driver {
	case "mysql":
		query = "SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE() ORDER BY table_name"
	case "postgres":
		query = "SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() ORDER BY table_name"
	case "sqlite":
		query = "SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name"
	case "clickhouse":
		query = "SELECT name FROM system.tables WHERE database = currentDatabase() ORDER BY name"
	default:
		return nil, errors.New("unsupported database driver")
	}
	rows, err := db.QueryContext(ctx, query)
	if err != nil { return nil, err }
	defer rows.Close()
	names := make([]string, 0)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil { return nil, err }
		names = append(names, name)
	}
	if err := rows.Err(); err != nil { return nil, err }
	return names, nil
}

func isDecimalDigits(value string) bool {
	if value == "" { return false }
	for _, digit := range value {
		if digit < '0' || digit > '9' { return false }
	}
	return true
}

func isShardTableName(rule adminstore.ShardRule, table string) bool {
	if table == rule.BaseTable { return true }
	prefix := rule.BaseTable + "_"
	if !strings.HasPrefix(table, prefix) || len(table) > 64 { return false }
	suffix := strings.TrimPrefix(table, prefix)
	switch rule.ShardType {
	case "year":
		return len(suffix) == 4 && isDecimalDigits(suffix)
	case "quarter":
		return len(suffix) == 6 && isDecimalDigits(suffix[:4]) && suffix[4] == 'Q' && suffix[5] >= '1' && suffix[5] <= '4'
	case "month":
		if len(suffix) != 6 { return false }
		month, monthErr := strconv.Atoi(suffix[4:])
		return isDecimalDigits(suffix[:4]) && isDecimalDigits(suffix[4:]) && monthErr == nil && month >= 1 && month <= 12
	case "week":
		if len(suffix) != 7 || suffix[4] != 'W' { return false }
		week, weekErr := strconv.Atoi(suffix[5:])
		return isDecimalDigits(suffix[:4]) && isDecimalDigits(suffix[5:]) && weekErr == nil && week >= 1 && week <= 53
	case "ten_day":
		if len(suffix) != 8 || suffix[6] != '_' { return false }
		yearMonth, yearMonthErr := strconv.Atoi(suffix[:6])
		segment, segmentErr := strconv.Atoi(suffix[7:])
		return isDecimalDigits(suffix[:6]) && yearMonthErr == nil && yearMonth >= 100001 && segmentErr == nil && isDecimalDigits(suffix[7:]) && segment >= 1 && segment <= 3
	case "id_hash":
		value, err := strconv.Atoi(suffix)
		return isDecimalDigits(suffix) && err == nil && value >= 0 && value < rule.HashShards
	case "id_range":
		value, err := strconv.ParseInt(suffix, 10, 64)
		return isDecimalDigits(suffix) && err == nil && value >= 0
	default:
		return false
	}
}

func (s *Server) listPhysicalShardTables(w http.ResponseWriter, r *http.Request) {
	if !s.requireShardStore(w) { return }
	rule, err := s.shards.GetShardRule(r.Context(), r.PathValue("id"))
	if err != nil { writeShardRuleError(w, err); return }
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	db, err := s.targetShardDB(ctx, rule.DataSourceID)
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "target_database_unavailable", "unable to connect to the configured target database")
		return
	}
	names, err := physicalTableNames(ctx, db, rule.Driver)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, "table_inspection_failed", "unable to inspect tables in the target database")
		return
	}
	items := make([]string, 0)
	for _, name := range names {
		if isShardTableName(rule, name) { items = append(items, name) }
	}
	writeJSON(w, http.StatusOK, map[string]any{"shard_rule_id": rule.ID, "items": items, "count": len(items)})
}

func (s *Server) checkPhysicalShardTable(w http.ResponseWriter, r *http.Request) {
	if !s.requireShardStore(w) { return }
	rule, err := s.shards.GetShardRule(r.Context(), r.PathValue("id"))
	if err != nil { writeShardRuleError(w, err); return }
	var input shardTableExistsRequest
	if err := decodeJSON(w, r, &input); err != nil { writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error()); return }
	input.TableName = strings.TrimSpace(input.TableName)
	if !isShardTableName(rule, input.TableName) {
		writeAPIError(w, http.StatusBadRequest, "invalid_table_name", "table_name must be the base table or a valid table with this rule's prefix")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	db, err := s.targetShardDB(ctx, rule.DataSourceID)
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "target_database_unavailable", "unable to connect to the configured target database")
		return
	}
	names, err := physicalTableNames(ctx, db, rule.Driver)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, "table_inspection_failed", "unable to inspect tables in the target database")
		return
	}
	exists := false
	for _, name := range names {
		if name == input.TableName { exists = true; break }
	}
	writeJSON(w, http.StatusOK, map[string]any{"table_name": input.TableName, "exists": exists})
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
