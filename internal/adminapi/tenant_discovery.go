package adminapi

import (
	"context"
	"database/sql"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/yuwen002/meteor-forge/pkg/tenant"

	_ "github.com/ClickHouse/clickhouse-go/v2"
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

const tenantDiscoveryLimit = 1000

type tenantDiscoveryPreviewRequest struct {
	ControlDataSourceID string `json:"control_data_source_id"`
}

type tenantDiscoveryItem struct {
	ExternalID       string `json:"external_id"`
	Driver           string `json:"driver,omitempty"`
	Action           string `json:"action"`
	RegisteredID     string `json:"registered_id,omitempty"`
	RegisteredStatus string `json:"registered_status,omitempty"`
}

type tenantDiscoveryPreview struct {
	ControlDataSourceID string                `json:"control_data_source_id"`
	Total               int                   `json:"total"`
	New                 int                   `json:"new"`
	AlreadyRegistered   int                   `json:"already_registered"`
	Items               []tenantDiscoveryItem `json:"items"`
}

// registerTenantDiscoveryRoutes exposes a read-only preview. Synchronization is
// deliberately a separate, explicitly confirmed operation and is not performed here.
func (s *Server) registerTenantDiscoveryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST "+apiPrefix+"/tenant-discovery/preview", s.previewTenantDiscovery)
}

func (s *Server) previewTenantDiscovery(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) || !s.requireTenantStore(w) {
		return
	}
	var input tenantDiscoveryPreviewRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "a control_data_source_id is required")
		return
	}
	input.ControlDataSourceID = strings.TrimSpace(input.ControlDataSourceID)
	if input.ControlDataSourceID == "" {
		writeAPIError(w, http.StatusBadRequest, "invalid_control_data_source", "a control data source must be selected")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	driver, dsn, err := s.store.ConnectionConfig(ctx, input.ControlDataSourceID)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "control_data_source_unavailable", "the selected control data source is unavailable")
		return
	}
	driverName := map[string]string{"mysql": "mysql", "postgres": "pgx", "sqlite": "sqlite", "clickhouse": "clickhouse"}[driver]
	if driverName == "" {
		writeAPIError(w, http.StatusBadRequest, "unsupported_control_driver", "the selected control data source driver is not supported")
		return
	}
	controlDB, err := sql.Open(driverName, dsn)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, "control_database_unavailable", "unable to connect to the control database")
		return
	}
	defer controlDB.Close()
	controlDB.SetMaxOpenConns(1)
	controlDB.SetMaxIdleConns(1)
	if err := controlDB.PingContext(ctx); err != nil {
		writeAPIError(w, http.StatusBadGateway, "control_database_unavailable", "unable to connect to the control database")
		return
	}

	discovered, err := tenant.LoadActiveTenants(ctx, controlDB)
	if err != nil {
		writeAPIError(w, http.StatusUnprocessableEntity, "tenant_discovery_failed", "unable to read active tenants from the configured control table")
		return
	}
	if len(discovered) > tenantDiscoveryLimit {
		writeAPIError(w, http.StatusUnprocessableEntity, "tenant_discovery_limit_exceeded", "the control table contains more than 1000 active tenants; narrow the source before previewing")
		return
	}
	registered, err := s.tenants.ListTenants(ctx)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "tenant_registry_unavailable", "unable to read the local tenant registry")
		return
	}
	byCode := make(map[string]struct {
		id     string
		status string
	}, len(registered))
	for _, item := range registered {
		byCode[strings.ToLower(strings.TrimSpace(item.Code))] = struct {
			id     string
			status string
		}{id: item.ID, status: item.Status}
	}

	result := tenantDiscoveryPreview{
		ControlDataSourceID: input.ControlDataSourceID,
		Items:               make([]tenantDiscoveryItem, 0, len(discovered)),
	}
	seen := make(map[string]struct{}, len(discovered))
	for _, item := range discovered {
		id := strings.TrimSpace(item.ID)
		key := strings.ToLower(id)
		if id == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			writeAPIError(w, http.StatusUnprocessableEntity, "duplicate_discovered_tenant", "the control table returned duplicate tenant identifiers")
			return
		}
		seen[key] = struct{}{}
		entry := tenantDiscoveryItem{ExternalID: id, Driver: item.Driver, Action: "create"}
		if current, exists := byCode[key]; exists {
			entry.Action = "already_registered"
			entry.RegisteredID = current.id
			entry.RegisteredStatus = current.status
			result.AlreadyRegistered++
		} else {
			result.New++
		}
		result.Items = append(result.Items, entry)
	}
	sort.Slice(result.Items, func(i, j int) bool { return result.Items[i].ExternalID < result.Items[j].ExternalID })
	result.Total = len(result.Items)
	writeJSON(w, http.StatusOK, result)
}
