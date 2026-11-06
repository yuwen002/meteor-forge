package adminapi

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/yuwen002/meteor-forge/internal/adminstore"
	"github.com/yuwen002/meteor-forge/pkg/tenant"
)

type tenantDiscoveryItem struct {
	ControlTenantID string `json:"control_tenant_id"`
	Driver string `json:"driver,omitempty"`
	SuggestedName string `json:"suggested_name"`
	SuggestedCode string `json:"suggested_code"`
	Registered bool `json:"registered"`
}

type tenantDiscoveryMapping struct {
	ControlTenantID string `json:"control_tenant_id"`
	Name string `json:"name"`
	Code string `json:"code"`
	DataSourceID string `json:"data_source_id"`
}

type tenantDiscoverySyncRequest struct {
	ControlDataSourceID string `json:"control_data_source_id"`
	Confirm bool `json:"confirm"`
	Items []tenantDiscoveryMapping `json:"items"`
}

var tenantCodeInvalid = regexp.MustCompile(`[^a-z0-9_-]+`)

func (s *Server) registerTenantDiscoveryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET "+apiPrefix+"/tenant-discovery/preview", s.previewTenantDiscovery)
	mux.HandleFunc("POST "+apiPrefix+"/tenant-discovery/sync", s.syncDiscoveredTenants)
}

func (s *Server) loadControlPlaneTenants(ctx context.Context, dataSourceID string) ([]tenant.TenantInfo, error) {
	if s.store == nil || s.pools == nil {
		return nil, errors.New("tenant discovery is unavailable")
	}
	if _, err := s.store.Get(ctx, dataSourceID); err != nil {
		return nil, err
	}
	queryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	db, err := s.pools.Get(queryCtx, dataSourceID, func(ctx context.Context, id string) (string, string, error) {
		driverName, dsn, err := s.store.ConnectionConfig(ctx, id)
		if err != nil { return "", "", err }
		sqlDriver := map[string]string{"mysql":"mysql", "postgres":"pgx", "sqlite":"sqlite", "clickhouse":"clickhouse"}[driverName]
		if sqlDriver == "" { return "", "", errors.New("unsupported control database driver") }
		return sqlDriver, dsn, nil
	})
	if err != nil { return nil, errors.New("unable to connect to the selected control data source") }
	items, err := tenant.LoadActiveTenants(queryCtx, db)
	if err != nil { return nil, errors.New("unable to read active tenants from the control database") }
	return items, nil
}

func suggestedTenantCode(id string) string {
	code := strings.ToLower(strings.TrimSpace(id))
	code = tenantCodeInvalid.ReplaceAllString(code, "-")
	return strings.Trim(code, "-_")
}

func (s *Server) previewTenantDiscovery(w http.ResponseWriter, r *http.Request) {
	dataSourceID := strings.TrimSpace(r.URL.Query().Get("data_source_id"))
	if dataSourceID == "" {
		writeAPIError(w, http.StatusBadRequest, "invalid_data_source", "data_source_id is required")
		return
	}
	discovered, err := s.loadControlPlaneTenants(r.Context(), dataSourceID)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, "tenant_discovery_failed", "unable to discover tenants from the selected control data source")
		return
	}
	local, err := s.tenants.ListTenants(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "unable to read registered tenants")
		return
	}
	registered := make(map[string]bool, len(local))
	for _, item := range local { registered[strings.ToLower(item.Code)] = true }
	items := make([]tenantDiscoveryItem, 0, len(discovered))
	for _, item := range discovered {
		code := suggestedTenantCode(item.ID)
		items = append(items, tenantDiscoveryItem{
			ControlTenantID: item.ID, Driver: item.Driver, SuggestedName: item.ID,
			SuggestedCode: code, Registered: registered[code],
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"control_data_source_id": dataSourceID, "items": items, "count": len(items),
		"note": "preview only; no tenant metadata has been changed",
	})
}

func (s *Server) syncDiscoveredTenants(w http.ResponseWriter, r *http.Request) {
	var input tenantDiscoverySyncRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !input.Confirm {
		writeAPIError(w, http.StatusBadRequest, "confirmation_required", "confirm must be true to import discovered tenants")
		return
	}
	if len(input.Items) == 0 || len(input.Items) > 500 {
		writeAPIError(w, http.StatusBadRequest, "invalid_items", "items must contain between 1 and 500 tenant mappings")
		return
	}
	discovered, err := s.loadControlPlaneTenants(r.Context(), strings.TrimSpace(input.ControlDataSourceID))
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, "tenant_discovery_failed", "unable to refresh tenants from the selected control data source")
		return
	}
	active := make(map[string]tenant.TenantInfo, len(discovered))
	for _, item := range discovered { active[item.ID] = item }
	seen := make(map[string]bool, len(input.Items))
	tenantInputs := make([]adminstore.TenantInput, 0, len(input.Items))
	for _, mapping := range input.Items {
		id := strings.TrimSpace(mapping.ControlTenantID)
		if id == "" || seen[id] {
			writeAPIError(w, http.StatusBadRequest, "invalid_mapping", "tenant IDs must be non-empty and unique")
			return
		}
		seen[id] = true
		sourceTenant, ok := active[id]
		if !ok {
			writeAPIError(w, http.StatusConflict, "discovery_changed", "one or more selected tenants are no longer active; refresh the preview")
			return
		}
		source, err := s.store.Get(r.Context(), strings.TrimSpace(mapping.DataSourceID))
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_target_data_source", "each tenant must map to an existing target data source")
			return
		}
		if sourceTenant.Driver != "" && source.Driver != sourceTenant.Driver {
			writeAPIError(w, http.StatusBadRequest, "driver_mismatch", "target data source driver does not match the discovered tenant driver")
			return
		}
		tenantInputs = append(tenantInputs, adminstore.TenantInput{
			Name: strings.TrimSpace(mapping.Name), Code: strings.TrimSpace(mapping.Code),
			DataSourceID: source.ID, Status: "active",
		})
	}
	created, err := s.tenants.CreateDiscoveredTenantsWithAudit(
		r.Context(), tenantInputs, authenticatedActor(r), "tenant-discovery:"+requestSourceForAudit(r),
	)
	if err != nil { writeTenantStoreError(w, err); return }
	writeJSON(w, http.StatusCreated, map[string]any{
		"items": created, "count": len(created),
		"message": "discovered tenants imported; existing tenants were not modified",
	})
}
