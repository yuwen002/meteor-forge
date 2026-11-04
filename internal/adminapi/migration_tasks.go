package adminapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/yuwen002/meteor-forge/internal/adminstore"
	"github.com/yuwen002/meteor-forge/pkg/migration"
)

type migrationTaskStore interface {
	CreateMigrationTask(context.Context, adminstore.MigrationTask) (adminstore.MigrationTask, error)
	GetMigrationTask(context.Context, string) (adminstore.MigrationTask, error)
	ListMigrationTasks(context.Context, int) ([]adminstore.MigrationTask, error)
	UpdateMigrationTask(context.Context, string, string, string, *time.Time, *time.Time) error
	RecoverInterruptedTasks(context.Context) error
}

type MigrationTaskManager struct {
	store migrationTaskStore
	run   func(context.Context, adminstore.MigrationTask) error

	mu      sync.Mutex
	cancels map[string]context.CancelFunc
	locks   map[string]*sync.Mutex
	closed  bool
	wg      sync.WaitGroup
}

func NewMigrationTaskManager(store migrationTaskStore, run func(context.Context, adminstore.MigrationTask) error) *MigrationTaskManager {
	return &MigrationTaskManager{
		store: store, run: run,
		cancels: make(map[string]context.CancelFunc),
		locks: make(map[string]*sync.Mutex),
	}
}

func newTaskID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil { return "", err }
	return hex.EncodeToString(value), nil
}

func (m *MigrationTaskManager) Submit(ctx context.Context, task adminstore.MigrationTask) (adminstore.MigrationTask, error) {
	id, err := newTaskID()
	if err != nil { return adminstore.MigrationTask{}, err }
	task.ID = id
	created, err := m.store.CreateMigrationTask(ctx, task)
	if err != nil { return adminstore.MigrationTask{}, err }

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		now := time.Now().UTC()
		_ = m.store.UpdateMigrationTask(context.Background(), id, "failed", "task manager is shutting down", nil, &now)
		return adminstore.MigrationTask{}, errors.New("migration task manager is closed")
	}
	taskCtx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	m.cancels[id] = cancel
	lock := m.locks[task.DataSourceID]
	if lock == nil {
		lock = &sync.Mutex{}
		m.locks[task.DataSourceID] = lock
	}
	m.wg.Add(1)
	m.mu.Unlock()

	go m.execute(taskCtx, cancel, lock, created)
	return created, nil
}

func (m *MigrationTaskManager) execute(ctx context.Context, cancel context.CancelFunc, lock *sync.Mutex, task adminstore.MigrationTask) {
	defer m.wg.Done()
	defer cancel()
	lock.Lock()
	defer lock.Unlock()

	now := time.Now().UTC()
	if err := ctx.Err(); err != nil {
		finished := time.Now().UTC()
		_ = m.store.UpdateMigrationTask(context.Background(), task.ID, "canceled", "task canceled before execution", nil, &finished)
		m.remove(task.ID)
		return
	}
	if err := m.store.UpdateMigrationTask(context.Background(), task.ID, "running", "migration is running", &now, nil); err != nil {
		m.remove(task.ID)
		return
	}

	err := m.run(ctx, task)
	finished := time.Now().UTC()
	status, message := "succeeded", "migration completed successfully"
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			status, message = "canceled", "migration task was canceled; some earlier migration steps may already have completed"
		} else if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			status, message = "failed", "migration task timed out; inspect target schema before retrying"
		} else {
			status, message = "failed", "migration operation failed; inspect target schema before retrying"
		}
	}
	_ = m.store.UpdateMigrationTask(context.Background(), task.ID, status, message, nil, &finished)
	m.remove(task.ID)
}

func (m *MigrationTaskManager) remove(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.cancels, id)
}

func (m *MigrationTaskManager) Cancel(id string) error {
	m.mu.Lock()
	cancel := m.cancels[id]
	m.mu.Unlock()
	if cancel == nil { return errors.New("migration task is not active") }
	cancel()
	return nil
}

func (m *MigrationTaskManager) Close() {
	m.mu.Lock()
	m.closed = true
	cancels := make([]context.CancelFunc, 0, len(m.cancels))
	for _, cancel := range m.cancels { cancels = append(cancels, cancel) }
	m.mu.Unlock()
	for _, cancel := range cancels { cancel() }
	m.wg.Wait()
}

// validateMigrationTask checks the complete applied history before any operation.
// It blocks checksum drift and missing local scripts for both forward and rollback tasks.
func validateMigrationTask(ctx context.Context, driver migration.Driver, loader migration.Loader, task adminstore.MigrationTask) error {
	if err := driver.EnsureTable(ctx); err != nil { return err }
	scripts, err := loader.LoadScripts(ctx)
	if err != nil { return err }
	records, err := driver.GetAppliedVersions(ctx)
	if err != nil { return err }

	byVersion := make(map[string]*migration.Script, len(scripts))
	indexByVersion := make(map[string]int, len(scripts))
	for i, script := range scripts {
		if script.UpSQL == "" { return errors.New("migration script has no up SQL") }
		byVersion[script.Version] = script
		indexByVersion[script.Version] = i
	}
	for _, record := range records {
		script, ok := byVersion[record.Version]
		if !ok { return errors.New("an applied migration script is missing locally") }
		if record.Checksum != "" && record.Checksum != script.Checksum() {
			return migration.ErrChecksumMismatch
		}
	}

	rollbackVersions := make([]string, 0)
	switch task.Action {
	case "down", "down_steps":
		sort.Slice(records, func(i, j int) bool { return records[i].Version > records[j].Version })
		count := 1
		if task.Action == "down_steps" { count = min(task.Steps, len(records)) }
		for i := 0; i < count; i++ { rollbackVersions = append(rollbackVersions, records[i].Version) }
	case "to_version":
		targetIndex := len(scripts) - 1
		if task.TargetVersion == "0" {
			targetIndex = -1
		} else {
			for i, script := range scripts {
				if script.Version == task.TargetVersion { targetIndex = i; break }
			}
			if task.TargetVersion != "" && targetIndex == len(scripts)-1 {
				found := false
				for _, script := range scripts { if script.Version == task.TargetVersion { found = true; break } }
				if !found { return migration.ErrInvalidVersion }
			}
		}
		currentIndex := -1
		for _, record := range records {
			if index, ok := indexByVersion[record.Version]; ok && index > currentIndex { currentIndex = index }
		}
		applied := make(map[string]bool, len(records))
		for _, record := range records { applied[record.Version] = true }
		for i := currentIndex; i > targetIndex; i-- {
			if applied[scripts[i].Version] { rollbackVersions = append(rollbackVersions, scripts[i].Version) }
		}
	}
	for _, version := range rollbackVersions {
		script := byVersion[version]
		if script == nil || script.DownSQL == "" {
			return errors.New("a migration selected for rollback has no down SQL")
		}
	}
	return nil
}

// runMigrationTask delegates execution to the existing core migration engine.
func runMigrationTask(ctx context.Context, driver migration.Driver, loader migration.Loader, task adminstore.MigrationTask) error {
	if err := validateMigrationTask(ctx, driver, loader, task); err != nil { return err }
	engine := migration.NewDefaultMigrator(loader, driver)
	switch task.Action {
	case "up":
		err := engine.Up(ctx)
		if errors.Is(err, migration.ErrNoPendingMigrations) { return nil }
		return err
	case "down":
		err := engine.Down(ctx)
		if errors.Is(err, migration.ErrNoPendingMigrations) { return nil }
		return err
	case "down_steps":
		err := engine.DownSteps(ctx, task.Steps)
		if errors.Is(err, migration.ErrNoPendingMigrations) { return nil }
		return err
	case "to_version":
		err := engine.ToVersion(ctx, task.TargetVersion)
		if errors.Is(err, migration.ErrNoPendingMigrations) { return nil }
		return err
	default:
		return errors.New("unsupported migration action")
	}
}
