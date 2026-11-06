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
	UpdateMigrationTaskProgress(context.Context, string, adminstore.MigrationTaskProgress) error
	RecoverInterruptedTasks(context.Context) error
}

type dataSourceTaskLock struct {
	mu   sync.Mutex
	refs int
}

type MigrationTaskManager struct {
	store migrationTaskStore
	run   func(context.Context, adminstore.MigrationTask) error

	mu      sync.Mutex
	cancels map[string]context.CancelFunc
	locks   map[string]*dataSourceTaskLock
	closed    bool
	wg        sync.WaitGroup
	closeOnce sync.Once
	done      chan struct{}
	runSlots  chan struct{}
}

func NewMigrationTaskManager(store migrationTaskStore, run func(context.Context, adminstore.MigrationTask) error) *MigrationTaskManager {
	return &MigrationTaskManager{
		store: store, run: run,
		cancels: make(map[string]context.CancelFunc),
		locks:   make(map[string]*dataSourceTaskLock),
		done:    make(chan struct{}),
		runSlots: make(chan struct{}, 4),
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
		lock = &dataSourceTaskLock{}
		m.locks[task.DataSourceID] = lock
	}
	// Count queued and running workers so the lock is removed only after the
	// final task for this data source has released it.
	lock.refs++
	m.wg.Add(1)
	m.mu.Unlock()

	go m.execute(taskCtx, cancel, lock, created)
	return created, nil
}

func (m *MigrationTaskManager) execute(ctx context.Context, cancel context.CancelFunc, lock *dataSourceTaskLock, task adminstore.MigrationTask) {
	defer func() {
		if recovered := recover(); recovered != nil {
			finished := time.Now().UTC()
			_ = m.store.UpdateMigrationTask(context.Background(), task.ID, "failed", "[WORKER_PANIC] Migration worker stopped unexpectedly. Next step: inspect the target schema and task audit before retrying.", nil, &finished)
		}
		m.remove(task.ID)
		m.releaseDataSourceLock(task.DataSourceID, lock)
		cancel()
		m.wg.Done()
	}()
	lock.mu.Lock()
	defer lock.mu.Unlock()

	// Bound concurrent migrations across different data sources. Tasks sharing
	// one data source still serialize on its dedicated lock above.
	select {
	case m.runSlots <- struct{}{}:
		defer func() { <-m.runSlots }()
	case <-ctx.Done():
		finished := time.Now().UTC()
		_ = m.store.UpdateMigrationTask(context.Background(), task.ID, "canceled", "task canceled while waiting for an execution slot", nil, &finished)
		return
	}

	now := time.Now().UTC()
	if err := ctx.Err(); err != nil {
		finished := time.Now().UTC()
		_ = m.store.UpdateMigrationTask(context.Background(), task.ID, "canceled", "task canceled before execution", nil, &finished)
		return
	}
	if err := m.store.UpdateMigrationTask(ctx, task.ID, "running", "migration is running", &now, nil); err != nil {
		finished := time.Now().UTC()
		_ = m.store.UpdateMigrationTask(context.Background(), task.ID, "failed", "unable to start migration worker; inspect task state before retrying", nil, &finished)
		return
	}

	err := m.run(ctx, task)
	finished := time.Now().UTC()
	status, message := "succeeded", "migration completed successfully"
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			status, message = "canceled", "migration task was canceled; some earlier migration steps may already have completed"
		} else if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			status, message = "failed", safeTaskFailure("migration", context.DeadlineExceeded)
		} else {
			status, message = "failed", safeTaskFailure("migration", err)
		}
	}
	_ = m.store.UpdateMigrationTask(context.Background(), task.ID, status, message, nil, &finished)
}

func (m *MigrationTaskManager) remove(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.cancels, id)
}

func (m *MigrationTaskManager) releaseDataSourceLock(dataSourceID string, lock *dataSourceTaskLock) {
	m.mu.Lock()
	defer m.mu.Unlock()
	lock.refs--
	if lock.refs == 0 && m.locks[dataSourceID] == lock {
		delete(m.locks, dataSourceID)
	}
}

func (m *MigrationTaskManager) Cancel(id string) error {
	m.mu.Lock()
	cancel := m.cancels[id]
	m.mu.Unlock()
	if cancel == nil { return errors.New("migration task is not active") }
	cancel()
	return nil
}

// CloseContext cancels queued/running workers and waits until they have released
// their per-data-source locks. A driver that ignores context cancellation may
// outlive the supplied deadline; callers must then keep shared pools alive.
func (m *MigrationTaskManager) CloseContext(ctx context.Context) error {
	m.closeOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		cancels := make([]context.CancelFunc, 0, len(m.cancels))
		for _, cancel := range m.cancels { cancels = append(cancels, cancel) }
		m.mu.Unlock()
		for _, cancel := range cancels { cancel() }
		go func() {
			m.wg.Wait()
			close(m.done)
		}()
	})
	select {
	case <-m.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close provides a bounded default shutdown wait for callers that do not
// manage their own process shutdown deadline.
func (m *MigrationTaskManager) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return m.CloseContext(ctx)
}

// runMigrationTask delegates execution to the existing core migration engine.
func runMigrationTask(ctx context.Context, driver migration.Driver, loader migration.Loader, task adminstore.MigrationTask, onProgress func(migration.Progress)) error {
	engine := migration.NewDefaultMigratorWithObserver(loader, driver, onProgress)
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
