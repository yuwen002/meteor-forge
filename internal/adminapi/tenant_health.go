package adminapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/yuwen002/meteor-forge/internal/adminstore"
	"github.com/yuwen002/meteor-forge/pkg/tenant"
)

const maxTenantHealthCheckBatch = 100

type tenantHealthCheckRequest struct {
	TenantIDs []string `json:"tenant_ids"`
}

func (s *Server) registerTenantHealthRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET "+apiPrefix+"/tenant-health", s.listTenantHealth)
	mux.HandleFunc("GET "+apiPrefix+"/tenant-health/settings", s.getTenantHealthSettings)
	mux.HandleFunc("PUT "+apiPrefix+"/tenant-health/settings", s.updateTenantHealthSettings)
	mux.HandleFunc("GET "+apiPrefix+"/tenant-health/history", s.listTenantHealthHistory)
	mux.HandleFunc("POST "+apiPrefix+"/tenant-health-checks", s.batchTenantHealthCheck)
}

func (s *Server) listTenantHealth(w http.ResponseWriter, r *http.Request) {
	if s.authStore == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "metadata_unavailable", "management metadata store is not configured")
		return
	}
	items, err := s.authStore.ListTenantConnectionHealth(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "tenant_health_query_failed", "unable to read tenant connection health")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}


func (s *Server) getTenantHealthSettings(w http.ResponseWriter, r *http.Request) {
	item, err := s.authStore.GetTenantHealthSettings(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "tenant_health_settings_failed", "unable to read tenant health settings")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) updateTenantHealthSettings(w http.ResponseWriter, r *http.Request) {
	var input adminstore.TenantHealthSettings
	if err := decodeJSON(w, r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	item, err := s.authStore.UpdateTenantHealthSettings(r.Context(), input)
	if errors.Is(err, adminstore.ErrTenantHealthInvalidInput) {
		writeAPIError(w, http.StatusBadRequest, "invalid_tenant_health_settings", "interval_seconds, timeout_seconds or history_retention_days is outside the supported range")
		return
	}
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "tenant_health_settings_failed", "unable to update tenant health settings")
		return
	}
	if s.healthScheduler != nil { s.healthScheduler.Refresh() }
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) listTenantHealthHistory(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	filter := adminstore.TenantHealthHistoryFilter{
		TenantID: strings.TrimSpace(query.Get("tenant_id")),
		Status: strings.TrimSpace(query.Get("status")),
		Source: strings.TrimSpace(query.Get("source")),
		Cursor: strings.TrimSpace(query.Get("cursor")),
	}
	if raw := query.Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 100 {
			writeAPIError(w, http.StatusBadRequest, "invalid_history_limit", "limit must be between 1 and 100")
			return
		}
		filter.Limit = limit
	}
	for _, item := range []struct{name string; target **time.Time}{
		{name:"from", target:&filter.From},
		{name:"to", target:&filter.To},
	} {
		if raw := query.Get(item.name); raw != "" {
			value, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				writeAPIError(w, http.StatusBadRequest, "invalid_history_time", "from and to must be RFC3339 timestamps")
				return
			}
			*item.target = &value
		}
	}
	page, err := s.authStore.ListTenantConnectionHealthHistory(r.Context(), filter)
	if errors.Is(err, adminstore.ErrTenantHealthInvalidInput) {
		writeAPIError(w, http.StatusBadRequest, "invalid_history_filter", "history filters or cursor are invalid")
		return
	}
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "tenant_health_history_failed", "unable to read tenant health history")
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) runTenantHealthCheck(ctx context.Context, tenantID string) (tenant.HealthResult, error) {
	return s.runTenantHealthCheckWithOptions(ctx, tenantID, 5*time.Second, "manual")
}

func (s *Server) runTenantHealthCheckWithOptions(ctx context.Context, tenantID string, timeout time.Duration, source string) (tenant.HealthResult, error) {
	item, err := s.tenants.GetTenant(ctx, tenantID)
	if err != nil {
		return tenant.HealthResult{}, err
	}
	started := time.Now().UTC()
	result := tenant.HealthResult{
		TenantID: item.ID,
		Status: tenant.HealthStatusUnhealthy,
		CheckedAt: started,
		Message: "connection check failed; verify the registered data source",
	}
	if s.healthChecker == nil || s.pools == nil || s.store == nil {
		result.Message = "connection check is unavailable"
	} else {
		checkCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		db, openErr := s.pools.Get(checkCtx, item.DataSourceID, func(ctx context.Context, id string) (string, string, error) {
			driver, dsn, err := s.store.ConnectionConfig(ctx, id)
			if err != nil { return "", "", err }
			driverName := map[string]string{"mysql":"mysql", "postgres":"pgx", "sqlite":"sqlite", "clickhouse":"clickhouse"}[driver]
			if driverName == "" { return "", "", errors.New("unsupported database driver") }
			return driverName, dsn, nil
		})
		if openErr == nil {
			checker, checkerErr := tenant.NewHealthChecker(timeout, 1)
			if checkerErr != nil { return tenant.HealthResult{}, checkerErr }
			result = checker.Check(checkCtx, item.ID, db)
			if result.Status == tenant.HealthStatusUnhealthy {
				s.pools.InvalidateIfCurrent(item.DataSourceID, db)
			}
		} else {
			result.DurationMS = time.Since(started).Milliseconds()
			if errors.Is(openErr, context.DeadlineExceeded) {
				result.Message = "connection check timed out"
			} else if errors.Is(openErr, context.Canceled) {
				result.Message = "connection check canceled"
			}
		}
	}
	persistCtx, persistCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer persistCancel()
	err = s.authStore.RecordTenantConnectionHealthWithSource(persistCtx, adminstore.TenantConnectionHealth{
		TenantID: result.TenantID,
		Status: string(result.Status),
		CheckedAt: result.CheckedAt,
		DurationMS: result.DurationMS,
		Message: result.Message,
	}, source)
	if err != nil {
		return tenant.HealthResult{}, errors.New("unable to persist tenant connection health")
	}
	return result, nil
}


func (s *Server) runScheduledTenantHealthChecks(ctx context.Context, settings adminstore.TenantHealthSettings) error {
	tenants, err := s.tenants.ListTenants(ctx)
	if err != nil { return err }
	jobs := make(chan string)
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for tenantID := range jobs {
				if ctx.Err() != nil { return }
				_, _ = s.runTenantHealthCheckWithOptions(ctx, tenantID, time.Duration(settings.TimeoutSeconds)*time.Second, "scheduled")
			}
		}()
	}
feed:
	for _, item := range tenants {
		if item.Status != "active" { continue }
		select {
		case jobs <- item.ID:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	return ctx.Err()
}

func (s *Server) batchTenantHealthCheck(w http.ResponseWriter, r *http.Request) {
	if !s.requireTenantStore(w) { return }
	var input tenantHealthCheckRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if len(input.TenantIDs) == 0 || len(input.TenantIDs) > maxTenantHealthCheckBatch {
		writeAPIError(w, http.StatusBadRequest, "invalid_tenant_ids", "tenant_ids must contain between 1 and 100 IDs")
		return
	}
	seen := make(map[string]struct{}, len(input.TenantIDs))
	for i := range input.TenantIDs {
		input.TenantIDs[i] = strings.TrimSpace(input.TenantIDs[i])
		if input.TenantIDs[i] == "" {
			writeAPIError(w, http.StatusBadRequest, "invalid_tenant_ids", "tenant IDs must be non-empty")
			return
		}
		if _, ok := seen[input.TenantIDs[i]]; ok {
			writeAPIError(w, http.StatusBadRequest, "duplicate_tenant_id", "tenant IDs must be unique")
			return
		}
		seen[input.TenantIDs[i]] = struct{}{}
	}
	results := make([]tenant.HealthResult, len(input.TenantIDs))
	errs := make([]error, len(input.TenantIDs))
	workers := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, id := range input.TenantIDs {
		wg.Add(1)
		go func(index int, tenantID string) {
			defer wg.Done()
			select {
			case workers <- struct{}{}:
				defer func() { <-workers }()
			case <-r.Context().Done():
				errs[index] = r.Context().Err()
				return
			}
			results[index], errs[index] = s.runTenantHealthCheck(r.Context(), tenantID)
		}(i, id)
	}
	wg.Wait()
	for _, err := range errs {
		if errors.Is(err, adminstore.ErrTenantNotFound) {
			writeAPIError(w, http.StatusNotFound, "tenant_not_found", "one or more tenants were not found")
			return
		}
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "tenant_health_check_failed", "unable to complete one or more tenant health checks")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": results, "count": len(results)})
}
