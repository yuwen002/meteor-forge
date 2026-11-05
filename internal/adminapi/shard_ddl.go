package adminapi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/yuwen002/meteor-forge/internal/adminstore"
)

type shardCloneRequest struct {
	SourceTable string `json:"source_table"`
	TargetTable string `json:"target_table"`
	Confirm bool `json:"confirm"`
}

type shardDDLTaskManager struct {
	store *adminstore.Store
	run func(context.Context, adminstore.ShardDDLTask) error
	mu sync.Mutex
	cancels map[string]context.CancelFunc
	locks map[string]*sync.Mutex
	slots chan struct{}
	wg sync.WaitGroup
	closed bool
}

func newShardDDLTaskManager(store *adminstore.Store, run func(context.Context, adminstore.ShardDDLTask) error) *shardDDLTaskManager {
	return &shardDDLTaskManager{store: store, run: run, cancels: make(map[string]context.CancelFunc), locks: make(map[string]*sync.Mutex), slots: make(chan struct{}, 2)}
}

func (m *shardDDLTaskManager) Submit(ctx context.Context, task adminstore.ShardDDLTask) (adminstore.ShardDDLTask, error) {
	created, err := m.store.CreateShardDDLTask(ctx, task)
	if err != nil { return adminstore.ShardDDLTask{}, err }
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		now := time.Now().UTC()
		_ = m.store.UpdateShardDDLTask(context.Background(), created.ID, "failed", "task manager is shutting down", nil, &now)
		return adminstore.ShardDDLTask{}, errors.New("shard DDL task manager is closed")
	}
	taskCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	m.cancels[created.ID] = cancel
	lock := m.locks[created.DataSourceID]
	if lock == nil { lock = &sync.Mutex{}; m.locks[created.DataSourceID] = lock }
	m.wg.Add(1)
	m.mu.Unlock()
	go m.execute(taskCtx, cancel, lock, created)
	return created, nil
}

func (m *shardDDLTaskManager) execute(ctx context.Context, cancel context.CancelFunc, lock *sync.Mutex, task adminstore.ShardDDLTask) {
	defer func() {
		if recover() != nil {
			finished := time.Now().UTC()
			_ = m.store.UpdateShardDDLTask(context.Background(), task.ID, "failed", "[WORKER_PANIC] Shard DDL worker stopped unexpectedly. Next step: inspect the target table and task audit before retrying.", nil, &finished)
		}
		m.mu.Lock()
		delete(m.cancels, task.ID)
		m.mu.Unlock()
		cancel()
		m.wg.Done()
	}()
	lock.Lock()
	defer lock.Unlock()
	select {
	case m.slots <- struct{}{}:
		defer func(){ <-m.slots }()
	case <-ctx.Done():
		finished := time.Now().UTC()
		_ = m.store.UpdateShardDDLTask(context.Background(), task.ID, "canceled", "task canceled while waiting for an execution slot", nil, &finished)
		return
	}
	if ctx.Err() != nil {
		finished := time.Now().UTC()
		_ = m.store.UpdateShardDDLTask(context.Background(), task.ID, "canceled", "task canceled before execution", nil, &finished)
		return
	}
	started := time.Now().UTC()
	if err := m.store.UpdateShardDDLTask(ctx, task.ID, "running", "physical table clone is running", &started, nil); err != nil { return }
	err := m.run(ctx, task)
	finished := time.Now().UTC()
	status, message := "succeeded", "physical table structure cloned successfully"
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			status, message = "canceled", "DDL task canceled; inspect target table before retrying"
		} else if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			status, message = "failed", safeTaskFailure("shard_ddl", context.DeadlineExceeded)
		} else {
			status, message = "failed", safeTaskFailure("shard_ddl", err)
		}
	}
	_ = m.store.UpdateShardDDLTask(context.Background(), task.ID, status, message, nil, &finished)
}

func (m *shardDDLTaskManager) Cancel(id string) error {
	m.mu.Lock()
	cancel := m.cancels[id]
	m.mu.Unlock()
	if cancel == nil { return errors.New("shard DDL task is not active") }
	cancel()
	return nil
}

func (m *shardDDLTaskManager) Close() error {
	m.mu.Lock()
	m.closed = true
	cancels := make([]context.CancelFunc, 0, len(m.cancels))
	for _, cancel := range m.cancels { cancels = append(cancels, cancel) }
	m.mu.Unlock()
	for _, cancel := range cancels { cancel() }
	done := make(chan struct{})
	go func(){ m.wg.Wait(); close(done) }()
	select {
	case <-done: return nil
	case <-time.After(10*time.Second): return context.DeadlineExceeded
	}
}

func shardCloneSQL(driver, target, source string) (string, string, error) {
	switch driver {
	case "mysql":
		return fmt.Sprintf("CREATE TABLE `%s` LIKE `%s`", target, source), "复制列定义、索引和生成列；不复制数据、外键定义或触发器。", nil
	case "postgres":
		return fmt.Sprintf(`CREATE TABLE "%s" (LIKE "%s" INCLUDING ALL)`, target, source), "复制列、默认值、检查/非空约束、索引和存储属性；不复制数据、外键约束或触发器。", nil
	case "sqlite":
		return fmt.Sprintf(`CREATE TABLE "%s" AS SELECT * FROM "%s" WHERE 0`, target, source), "SQLite CTAS 仅复制列结构，不复制主键、约束、索引和触发器；请在生产使用前自行补齐这些对象。", nil
	case "clickhouse":
		return fmt.Sprintf("CREATE TABLE `%s` AS `%s`", target, source), "复制表结构和引擎定义；不复制数据。", nil
	default:
		return "", "", errors.New("unsupported database driver")
	}
}

func (s *Server) registerShardDDLRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST "+apiPrefix+"/shard-rules/{id}/clone-preview", s.previewShardClone)
	mux.HandleFunc("POST "+apiPrefix+"/shard-rules/{id}/clone-tasks", s.createShardCloneTask)
	mux.HandleFunc("GET "+apiPrefix+"/shard-ddl-tasks", s.listShardDDLTasks)
	mux.HandleFunc("GET "+apiPrefix+"/shard-ddl-tasks/{id}", s.getShardDDLTask)
	mux.HandleFunc("GET "+apiPrefix+"/shard-ddl-tasks/{id}/audit", s.listShardDDLAudit)
	mux.HandleFunc("POST "+apiPrefix+"/shard-ddl-tasks/{id}/cancel", s.cancelShardDDLTask)
}

func (s *Server) validateShardClone(rule adminstore.ShardRule, input shardCloneRequest) (string, string, error) {
	input.SourceTable = strings.TrimSpace(input.SourceTable)
	input.TargetTable = strings.TrimSpace(input.TargetTable)
	if !isShardTableName(rule, input.SourceTable) || !isShardTableName(rule, input.TargetTable) || input.TargetTable == rule.BaseTable || input.SourceTable == input.TargetTable {
		return "", "", errors.New("source_table and target_table must match this rule's physical table naming convention and be different")
	}
	if _, _, err := shardCloneSQL(rule.Driver, input.TargetTable, input.SourceTable); err != nil { return "", "", err }
	return input.SourceTable, input.TargetTable, nil
}

func (s *Server) previewShardClone(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) || !s.requireShardStore(w) { return }
	rule, err := s.shards.GetShardRule(r.Context(), r.PathValue("id"))
	if err != nil { writeShardRuleError(w, err); return }
	var input shardCloneRequest
	if err := decodeJSON(w, r, &input); err != nil { writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error()); return }
	source, target, err := s.validateShardClone(rule, input)
	if err != nil { writeAPIError(w, http.StatusBadRequest, "invalid_clone_request", err.Error()); return }
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	db, err := s.targetShardDB(ctx, rule.DataSourceID)
	if err != nil { writeAPIError(w, http.StatusServiceUnavailable, "target_database_unavailable", "unable to connect to the configured target database"); return }
	names, err := physicalTableNames(ctx, db, rule.Driver)
	if err != nil { writeAPIError(w, http.StatusBadGateway, "table_inspection_failed", "unable to inspect target database tables"); return }
	sourceExists, targetExists := false, false
	for _, name := range names { if name == source { sourceExists = true }; if name == target { targetExists = true } }
	if !sourceExists { writeAPIError(w, http.StatusConflict, "clone_source_missing", "source physical table does not exist"); return }
	if targetExists { writeAPIError(w, http.StatusConflict, "clone_target_exists", "target physical table already exists"); return }
	sql, warning, err := shardCloneSQL(rule.Driver, target, source)
	if err != nil { writeAPIError(w, http.StatusBadRequest, "unsupported_driver", "the selected database driver does not support table cloning"); return }
	writeJSON(w, http.StatusOK, map[string]any{"shard_rule_id": rule.ID, "driver": rule.Driver, "source_table": source, "target_table": target, "sql": sql, "copies_data": false, "warning": warning})
}

func (s *Server) createShardCloneTask(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) || !s.requireShardStore(w) { return }
	if s.shardDDL == nil { writeAPIError(w, http.StatusServiceUnavailable, "task_manager_unavailable", "shard DDL task manager is unavailable"); return }
	rule, err := s.shards.GetShardRule(r.Context(), r.PathValue("id"))
	if err != nil { writeShardRuleError(w, err); return }
	var input shardCloneRequest
	if err := decodeJSON(w, r, &input); err != nil { writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error()); return }
	if !input.Confirm { writeAPIError(w, http.StatusBadRequest, "confirmation_required", "physical table DDL requires confirm=true after reviewing the preview"); return }
	source, target, err := s.validateShardClone(rule, input)
	if err != nil { writeAPIError(w, http.StatusBadRequest, "invalid_clone_request", err.Error()); return }
	requestSource, _, _ := net.SplitHostPort(r.RemoteAddr)
	if requestSource == "" { requestSource = r.RemoteAddr }
	task, err := s.shardDDL.Submit(r.Context(), adminstore.ShardDDLTask{ShardRuleID: rule.ID, DataSourceID: rule.DataSourceID, Action: "clone", SourceTable: source, TargetTable: target, RequestSource: requestSource, Actor: authenticatedActor(r)})
	if err != nil {
		if errors.Is(err, adminstore.ErrActiveTasks) {
			writeAPIError(w, http.StatusConflict, "data_source_has_active_migration_tasks", "shard DDL tasks cannot be queued while migration tasks are active")
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "task_submission_failed", "unable to create shard DDL task")
		return
	}
	writeJSON(w, http.StatusAccepted, task)
}

func (s *Server) listShardDDLTasks(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) { return }
	items, err := s.store.(*adminstore.Store).ListShardDDLTasks(r.Context(), 50)
	if err != nil { writeAPIError(w, http.StatusInternalServerError, "task_query_failed", "unable to list shard DDL tasks"); return }
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) getShardDDLTask(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) { return }
	task, err := s.store.(*adminstore.Store).GetShardDDLTask(r.Context(), r.PathValue("id"))
	if errors.Is(err, adminstore.ErrShardDDLTaskNotFound) { writeAPIError(w, http.StatusNotFound, "not_found", "shard DDL task not found"); return }
	if err != nil { writeAPIError(w, http.StatusInternalServerError, "task_query_failed", "unable to read shard DDL task"); return }
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) listShardDDLAudit(w http.ResponseWriter, r *http.Request) {
	if !s.requireStore(w) { return }
	items, err := s.store.(*adminstore.Store).ListShardDDLAuditEvents(r.Context(), r.PathValue("id"))
	if errors.Is(err, adminstore.ErrShardDDLTaskNotFound) { writeAPIError(w, http.StatusNotFound, "not_found", "shard DDL task not found"); return }
	if err != nil { writeAPIError(w, http.StatusInternalServerError, "audit_query_failed", "unable to read shard DDL audit events"); return }
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) cancelShardDDLTask(w http.ResponseWriter, r *http.Request) {
	if s.shardDDL == nil { writeAPIError(w, http.StatusServiceUnavailable, "task_manager_unavailable", "shard DDL task manager is unavailable"); return }
	if err := s.shardDDL.Cancel(r.PathValue("id")); err != nil { writeAPIError(w, http.StatusConflict, "task_not_active", "shard DDL task is not active in this service instance"); return }
	writeJSON(w, http.StatusAccepted, map[string]string{"status":"cancel_requested"})
}
