package adminapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
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
	m.mu.Unlock()

	go m.execute(taskCtx, cancel, lock, created)
	return created, nil
}

func (m *MigrationTaskManager) execute(ctx context.Context, cancel context.CancelFunc, lock *sync.Mutex, task adminstore.MigrationTask) {
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
}

// runMigrationTask delegates execution to the existing core migration engine.
func runMigrationTask(ctx context.Context, driver migration.Driver, loader migration.Loader, task adminstore.MigrationTask) error {
	engine := migration.NewDefaultMigrator(loader, driver)
	switch task.Action {
	case "up":
		return engine.Up(ctx)
	case "down":
		return engine.Down(ctx)
	case "down_steps":
		return engine.DownSteps(ctx, task.Steps)
	case "to_version":
		return engine.ToVersion(ctx, task.TargetVersion)
	default:
		return errors.New("unsupported migration action")
	}
}
