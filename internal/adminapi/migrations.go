package adminapi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/yuwen002/meteor-forge/internal/adminstore"
	clickhousedriver "github.com/yuwen002/meteor-forge/pkg/driver/clickhouse"
	mysqldriver "github.com/yuwen002/meteor-forge/pkg/driver/mysql"
	postgresdriver "github.com/yuwen002/meteor-forge/pkg/driver/postgres"
	sqlitedriver "github.com/yuwen002/meteor-forge/pkg/driver/sqlite"
	"github.com/yuwen002/meteor-forge/pkg/migration"
)

type migrationStatusItem struct {
	Version     string `json:"version"`
	Description string `json:"description"`
	IsApplied   bool   `json:"is_applied"`
}

type migrationStatusResponse struct {
	DataSourceID string                `json:"data_source_id"`
	Items        []migrationStatusItem `json:"items"`
	AppliedCount int                   `json:"applied_count"`
	PendingCount int                   `json:"pending_count"`
}

type migrationPlanItem struct {
	Version     string `json:"version"`
	Description string `json:"description"`
	Action      string `json:"action"`
	Reversible  bool   `json:"reversible"`
	Checksum    string `json:"checksum"`
}

type migrationPreviewResponse struct {
	DataSourceID string              `json:"data_source_id"`
	Items        []migrationPlanItem `json:"items"`
	ChangeCount  int                 `json:"change_count"`
}

func (s *Server) registerMigrationRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET "+apiPrefix+"/datasources/{id}/migrations/status", s.migrationStatus)
	mux.HandleFunc("POST "+apiPrefix+"/datasources/{id}/migrations/preview", s.previewMigrations)
	mux.HandleFunc("POST "+apiPrefix+"/datasources/{id}/migrations/tasks", s.createMigrationTask)
	mux.HandleFunc("POST "+apiPrefix+"/tenants/{id}/migrations/tasks", s.createTenantMigrationTask)
	mux.HandleFunc("POST "+apiPrefix+"/tenant-migration-batches", s.createTenantMigrationBatch)
	mux.HandleFunc("GET "+apiPrefix+"/tenant-migration-batches/{id}", s.getTenantMigrationBatch)
	mux.HandleFunc("GET "+apiPrefix+"/migration-tasks", s.listMigrationTasks)
	mux.HandleFunc("GET "+apiPrefix+"/migration-tasks/{id}", s.getMigrationTask)
	mux.HandleFunc("POST "+apiPrefix+"/migration-tasks/{id}/cancel", s.cancelMigrationTask)
	mux.HandleFunc("GET "+apiPrefix+"/migration-tasks/{id}/audit", s.listMigrationTaskAudit)
}

func (s *Server) migrationStatus(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	if !s.requireMigrationDirectory(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	dbDriver, err := s.migrationDriver(ctx, r.PathValue("id"))
	if err != nil {
		writeMigrationSetupError(w, err)
		return
	}
	loader := migration.NewFileSystemLoader(os.DirFS(s.migrationDir), ".")
	statuses, err := migration.NewDefaultMigrator(loader, dbDriver).Status(ctx)
	if err != nil {
		writeAPIError(w, http.StatusUnprocessableEntity, "migration_status_failed", "unable to read migration status; verify the migration directory and database schema")
		return
	}

	response := migrationStatusResponse{
		DataSourceID: r.PathValue("id"),
		Items: make([]migrationStatusItem, 0, len(statuses)),
	}
	for _, item := range statuses {
		response.Items = append(response.Items, migrationStatusItem{
			Version: item.Version, Description: item.Description, IsApplied: item.IsApplied,
		})
		if item.IsApplied {
			response.AppliedCount++
		} else {
			response.PendingCount++
		}
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) previewMigrations(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
		return
	}
	if !s.requireMigrationDirectory(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	dbDriver, err := s.migrationDriver(ctx, r.PathValue("id"))
	if err != nil {
		writeMigrationSetupError(w, err)
		return
	}
	if err := dbDriver.EnsureTable(ctx); err != nil {
		writeAPIError(w, http.StatusUnprocessableEntity, "migration_history_unavailable", "unable to initialize or read migration history")
		return
	}

	loader := migration.NewFileSystemLoader(os.DirFS(s.migrationDir), ".")
	scripts, err := loader.LoadScripts(ctx)
	if err != nil {
		writeAPIError(w, http.StatusUnprocessableEntity, "migration_scripts_unavailable", "unable to load migration scripts from the configured directory")
		return
	}
	records, err := dbDriver.GetAppliedVersions(ctx)
	if err != nil {
		writeAPIError(w, http.StatusUnprocessableEntity, "migration_history_unavailable", "unable to read migration history")
		return
	}

	scriptByVersion := make(map[string]*migration.Script, len(scripts))
	for _, script := range scripts {
		if script.UpSQL == "" {
			writeAPIError(w, http.StatusUnprocessableEntity, "invalid_migration_script", "a migration script is missing its up SQL")
			return
		}
		scriptByVersion[script.Version] = script
	}
	applied := make(map[string]string, len(records))
	for _, record := range records {
		script, ok := scriptByVersion[record.Version]
		if !ok {
			writeAPIError(w, http.StatusConflict, "applied_migration_script_missing", "an applied migration is missing from the configured migration directory")
			return
		}
		if record.Checksum != "" && record.Checksum != script.Checksum() {
			writeAPIError(w, http.StatusConflict, "migration_checksum_mismatch", "an applied migration differs from the local script; execution is blocked")
			return
		}
		applied[record.Version] = record.Checksum
	}
	response := migrationPreviewResponse{DataSourceID: r.PathValue("id"), Items: make([]migrationPlanItem, 0, len(scripts))}
	for _, script := range scripts {
		checksum := script.Checksum()
		if _, ok := applied[script.Version]; ok {
			continue
		}
		response.Items = append(response.Items, migrationPlanItem{
			Version: script.Version, Description: script.Description, Action: "up",
			Reversible: script.DownSQL != "", Checksum: checksum,
		})
	}
	response.ChangeCount = len(response.Items)
	writeJSON(w, http.StatusOK, response)
}


type createMigrationTaskRequest struct {
	Action        string `json:"action"`
	TargetVersion string `json:"target_version"`
	Steps         int    `json:"steps"`
	Confirm       bool   `json:"confirm"`
}

func (s *Server) createMigrationTask(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) || s.tasks == nil { return }
	if !s.requireMigrationDirectory(w) { return }
	dataSourceID := r.PathValue("id")
	if _, err := s.store.Get(r.Context(), dataSourceID); err != nil { writeStoreError(w, err); return }
	var request createMigrationTaskRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if request.Action != "up" {
		if session, ok := adminSessionFromRequest(r); !ok || session.Admin.Role != "administrator" {
			writeAPIError(w, http.StatusForbidden, "administrator_required", "rollback and version-change operations require an administrator")
			return
		}
	}
	switch request.Action {
	case "up":
	case "down":
		if !request.Confirm { writeAPIError(w, http.StatusBadRequest, "confirmation_required", "rollback requires explicit confirmation"); return }
	case "down_steps":
		if !request.Confirm { writeAPIError(w, http.StatusBadRequest, "confirmation_required", "rollback requires explicit confirmation"); return }
		if request.Steps < 1 || request.Steps > 100 { writeAPIError(w, http.StatusBadRequest, "invalid_steps", "steps must be between 1 and 100"); return }
	case "to_version":
		if !request.Confirm { writeAPIError(w, http.StatusBadRequest, "confirmation_required", "version changes require explicit confirmation"); return }
		if request.TargetVersion == "" { writeAPIError(w, http.StatusBadRequest, "invalid_version", "target_version is required; use 0 to roll back all migrations"); return }
	default:
		writeAPIError(w, http.StatusBadRequest, "invalid_action", "action must be up, down, down_steps, or to_version")
		return
	}
	requestSource, _, _ := net.SplitHostPort(r.RemoteAddr)
	if requestSource == "" { requestSource = r.RemoteAddr }
	task, err := s.tasks.Submit(r.Context(), adminstore.MigrationTask{
		DataSourceID: dataSourceID, Action: request.Action,
		TargetVersion: request.TargetVersion, Steps: request.Steps,
		RequestSource: requestSource, Actor: authenticatedActor(r),
	})
	if err != nil {
		if errors.Is(err, adminstore.ErrActiveShardDDLTasks) {
			writeAPIError(w, http.StatusConflict, "data_source_has_active_shard_ddl_tasks", "migration tasks cannot be queued while shard DDL tasks are active")
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "task_submission_failed", "unable to create migration task")
		return
	}
	writeJSON(w, http.StatusAccepted, task)
}

func (s *Server) listMigrationTasks(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) { return }
	store, ok := s.store.(migrationTaskStore)
	if !ok { writeAPIError(w, http.StatusServiceUnavailable, "task_store_unavailable", "migration task storage is unavailable"); return }
	items, err := store.ListMigrationTasks(r.Context(), 50)
	if err != nil { writeAPIError(w, http.StatusInternalServerError, "task_query_failed", "unable to list migration tasks"); return }
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) getMigrationTask(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) { return }
	store, ok := s.store.(migrationTaskStore)
	if !ok { writeAPIError(w, http.StatusServiceUnavailable, "task_store_unavailable", "migration task storage is unavailable"); return }
	item, err := store.GetMigrationTask(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, adminstore.ErrNotFound) { writeAPIError(w, http.StatusNotFound, "not_found", "migration task not found") } else { writeAPIError(w, http.StatusInternalServerError, "task_query_failed", "unable to read migration task") }
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) cancelMigrationTask(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) || s.tasks == nil { return }
	id := r.PathValue("id")
	if err := s.tasks.Cancel(id); err != nil {
		writeAPIError(w, http.StatusConflict, "task_not_active", "migration task is not currently active")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"id": id, "status": "cancel_requested"})
}

func (s *Server) requireMigrationDirectory(w http.ResponseWriter) bool {
	info, err := os.Stat(s.migrationDir)
	if err != nil || !info.IsDir() {
		writeAPIError(w, http.StatusServiceUnavailable, "migration_directory_unavailable", "configured migration directory does not exist or is not a directory")
		return false
	}
	return true
}

func (s *Server) migrationDriver(ctx context.Context, id string) (migration.Driver, error) {
	// Read the driver from metadata independently of the pool factory. The
	// factory is skipped on a pool-cache hit, so assigning configuredDriver
	// only inside it breaks subsequent migrations for the same data source.
	source, err := s.store.Get(ctx, id)
	if err != nil { return nil, err }
	configuredDriver := source.Driver

	db, err := s.pools.Get(ctx, id, func(ctx context.Context, id string) (string, string, error) {
		driver, dsn, err := s.store.ConnectionConfig(ctx, id)
		if err != nil { return "", "", err }
		sqlDriver := map[string]string{
			"mysql": "mysql", "postgres": "pgx", "sqlite": "sqlite", "clickhouse": "clickhouse",
		}[driver]
		if sqlDriver == "" {
			return "", "", errors.New("unsupported database driver")
		}
		return sqlDriver, dsn, nil
	})
	if err != nil { return nil, err }

	switch configuredDriver {
	case "mysql":
		return mysqldriver.NewDriver(db, ""), nil
	case "postgres":
		return postgresdriver.NewDriver(db, ""), nil
	case "sqlite":
		return sqlitedriver.NewDriver(db, ""), nil
	case "clickhouse":
		return clickhousedriver.NewDriver(db, ""), nil
	default:
		return nil, errors.New("unsupported database driver")
	}
}

func writeMigrationSetupError(w http.ResponseWriter, err error) {
	if errors.Is(err, adminstore.ErrNotFound) {
		writeStoreError(w, err)
		return
	}
	writeAPIError(w, http.StatusUnprocessableEntity, "migration_connection_failed", "unable to connect to the selected data source")
}

func (s *Server) listMigrationTaskAudit(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) { return }
	store, ok := s.store.(interface {
		ListMigrationAuditEvents(context.Context, string) ([]adminstore.MigrationAuditEvent, error)
	})
	if !ok { writeAPIError(w, http.StatusServiceUnavailable, "audit_store_unavailable", "migration audit storage is unavailable"); return }
	items, err := store.ListMigrationAuditEvents(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, adminstore.ErrNotFound) { writeAPIError(w, http.StatusNotFound, "not_found", "migration task not found") } else { writeAPIError(w, http.StatusInternalServerError, "audit_query_failed", "unable to read migration audit events") }
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
