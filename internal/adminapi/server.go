// Package adminapi exposes the HTTP boundary for the meteor-forge management
// plane. Business operations are added through services in later milestones;
// core migration, tenant, and sharding packages remain HTTP-agnostic.
package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"net/http"
	"time"

	"github.com/yuwen002/meteor-forge/internal/adminstore"
	"github.com/yuwen002/meteor-forge/pkg/migration"
	"github.com/yuwen002/meteor-forge/pkg/tenant"
)

const apiPrefix = "/api/v1"

// Server owns the management API routes and middleware.
type Server struct {
	handler http.Handler
	store   dataSourceStore
	authStore *adminstore.Store
	loginLimiter *loginLimiter
	tenants tenantStore
	tenantConnections *tenantConnectionResolver
	shards shardRuleStore
	pools        *PoolManager
	tasks        *MigrationTaskManager
	shardDDL     *shardDDLTaskManager
	migrationDir string
}

// NewServer builds the API without a metadata store, useful for health-only embedding.
func NewServer() *Server {
	return NewServerWithStore(nil)
}

// NewServerWithStore builds an API server backed by the management metadata store.
func NewServerWithStore(store *adminstore.Store) *Server {
	return NewServerWithMigrationDir(store, "migrations")
}

// NewServerWithMigrationDir builds an API server using the supplied migration script directory.
func NewServerWithMigrationDir(store *adminstore.Store, migrationDir string) *Server {
	if migrationDir == "" { migrationDir = "migrations" }
	mux := http.NewServeMux()
	server := &Server{pools: NewPoolManager(), migrationDir: migrationDir, authStore: store, loginLimiter: newLoginLimiter()}
	if store != nil {
		server.store = store
		server.tenants = store
		server.shards = store
		server.tenantConnections = newTenantConnectionResolver(tenant.HeaderResolver{}, store, store, server.pools)
		server.shardDDL = newShardDDLTaskManager(store, func(ctx context.Context, task adminstore.ShardDDLTask) error {
			rule, err := store.GetShardRule(ctx, task.ShardRuleID)
			if err != nil { return err }
			db, err := server.targetShardDB(ctx, task.DataSourceID)
			if err != nil { return err }
			names, err := physicalTableNames(ctx, db, rule.Driver)
			if err != nil { return err }
			sourceExists, targetExists := false, false
			for _, name := range names { if name == task.SourceTable { sourceExists = true }; if name == task.TargetTable { targetExists = true } }
			if !sourceExists { return errors.New("source physical table does not exist") }
			if targetExists { return errors.New("target physical table already exists") }
			statement, _, err := shardCloneSQL(rule.Driver, task.TargetTable, task.SourceTable)
			if err != nil { return err }
			_, err = db.ExecContext(ctx, statement)
			return err
		})
		recoveryDDLContext, recoveryDDLCancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = store.RecoverInterruptedShardDDLTasks(recoveryDDLContext)
		recoveryDDLCancel()
		server.tasks = NewMigrationTaskManager(store, func(ctx context.Context, task adminstore.MigrationTask) error {
			driver, err := server.migrationDriver(ctx, task.DataSourceID)
			if err != nil { return err }
			loader := migration.NewFileSystemLoader(os.DirFS(server.migrationDir), ".")
			return runMigrationTask(ctx, driver, loader, task, func(progress migration.Progress) {
				progressCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_ = store.UpdateMigrationTaskProgress(progressCtx, task.ID, adminstore.MigrationTaskProgress{
					CurrentVersion: progress.CurrentVersion,
					CompletedSteps: progress.CompletedSteps,
					TotalSteps: progress.TotalSteps,
					CurrentAction: progress.CurrentAction,
				})
			})
		})
		recoveryCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = store.RecoverInterruptedTasks(recoveryCtx)
		cancel()
	}
	mux.HandleFunc("GET "+apiPrefix+"/health", healthHandler)
	server.registerDataSourceRoutes(mux)
	server.registerTenantRoutes(mux)
	server.registerMigrationRoutes(mux)
	server.registerShardingRoutes(mux)
	server.registerShardDDLRoutes(mux)
	server.registerAuditRoutes(mux)
	server.registerAuthRoutes(mux)
	var protected bool
	if store != nil {
		checkCtx, checkCancel := context.WithTimeout(context.Background(), 2*time.Second)
		var checkErr error
		protected, checkErr = store.HasAdmin(checkCtx)
		if checkErr != nil { protected = true }
		checkCancel()
	}
	var handler http.Handler = mux
	if protected { handler = server.withAuthentication(handler) }
	server.handler = withCommonHeaders(handler)
	return server
}

// Handler returns the HTTP handler. It is exposed to support embedding and tests.
func (s *Server) Handler() http.Handler { return s.handler }

// Close releases all cached target database pools owned by the API server.
func (s *Server) Close() error {
	if s.shardDDL != nil {
		if err := s.shardDDL.Close(); err != nil { return err }
	}
	if s.tasks != nil {
		if err := s.tasks.Close(); err != nil {
			// Keep target pools alive while a driver that ignores cancellation may
			// still be using them. The process supervisor enforces final shutdown.
			return err
		}
	}
	if s.pools == nil { return nil }
	return s.pools.Close()
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "service": "meteor-forge-admin", "api": "v1",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
}

func withCommonHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
