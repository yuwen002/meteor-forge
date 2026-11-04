package adminapi

import (
	"context"
	"errors"
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
}

func (s *Server) migrationStatus(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) {
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

func (s *Server) migrationDriver(ctx context.Context, id string) (migration.Driver, error) {
	var configuredDriver string
	db, err := s.pools.Get(ctx, id, func(ctx context.Context, id string) (string, string, error) {
		driver, dsn, err := s.store.ConnectionConfig(ctx, id)
		if err != nil {
			return "", "", err
		}
		configuredDriver = driver
		sqlDriver := map[string]string{
			"mysql": "mysql", "postgres": "pgx", "sqlite": "sqlite", "clickhouse": "clickhouse",
		}[driver]
		if sqlDriver == "" {
			return "", "", errors.New("unsupported database driver")
		}
		return sqlDriver, dsn, nil
	})
	if err != nil {
		return nil, err
	}

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
