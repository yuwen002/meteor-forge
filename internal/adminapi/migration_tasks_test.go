package adminapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yuwen002/meteor-forge/internal/adminstore"
)

func TestMigrationTaskUpAndConfirmedDown(t *testing.T) {
	dir := t.TempDir()
	writeMigrationPair(t, dir, "000001_create_task_items",
		"CREATE TABLE task_items (id INTEGER PRIMARY KEY)",
		"DROP TABLE task_items",
	)
	target := filepath.Join(t.TempDir(), "target.db")
	store, err := adminstore.Open(context.Background(), ":memory:", apiTestKey)
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = store.Close() })
	source, err := store.Create(context.Background(), adminstore.DataSourceInput{
		Name: "task target", Driver: "sqlite", DSN: target,
	})
	if err != nil { t.Fatal(err) }
	server := NewServerWithMigrationDir(store, dir)
	t.Cleanup(func() { _ = server.Close() })

	up := httptest.NewRequest(http.MethodPost, "/api/v1/datasources/"+source.ID+"/migrations/tasks", strings.NewReader(`{"action":"up"}`))
	up.Header.Set("Content-Type", "application/json")
	upRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(upRec, up)
	if upRec.Code != http.StatusAccepted { t.Fatalf("submit up status=%d body=%s", upRec.Code, upRec.Body.String()) }
	var created struct { ID string `json:"id"` }
	if err := json.Unmarshal(upRec.Body.Bytes(), &created); err != nil { t.Fatal(err) }
	waitForTaskStatus(t, store, created.ID, "succeeded")
	completedTask, err := store.GetMigrationTask(context.Background(), created.ID)
	if err != nil { t.Fatal(err) }
	if completedTask.CurrentVersion != "000001" || completedTask.CompletedSteps != 1 ||
		completedTask.TotalSteps != 1 || completedTask.CurrentAction != "up" ||
		completedTask.ProgressUpdatedAt == nil {
		t.Fatalf("expected completed migration progress to be persisted, got %+v", completedTask)
	}

	db, err := sql.Open("sqlite", target)
	if err != nil { t.Fatal(err) }
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='task_items'").Scan(&count); err != nil { t.Fatal(err) }
	if count != 1 { t.Fatal("Up task did not create target table") }
	_ = db.Close()

	downWithoutConfirmation := httptest.NewRequest(http.MethodPost, "/api/v1/datasources/"+source.ID+"/migrations/tasks", strings.NewReader(`{"action":"down"}`))
	downWithoutConfirmation.Header.Set("Content-Type", "application/json")
	downRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(downRec, downWithoutConfirmation)
	if downRec.Code != http.StatusBadRequest { t.Fatalf("unconfirmed Down status=%d body=%s", downRec.Code, downRec.Body.String()) }

	down := httptest.NewRequest(http.MethodPost, "/api/v1/datasources/"+source.ID+"/migrations/tasks", strings.NewReader(`{"action":"down","confirm":true}`))
	down.Header.Set("Content-Type", "application/json")
	downRec = httptest.NewRecorder()
	server.Handler().ServeHTTP(downRec, down)
	if downRec.Code != http.StatusAccepted { t.Fatalf("submit down status=%d body=%s", downRec.Code, downRec.Body.String()) }
	created.ID = ""
	if err := json.Unmarshal(downRec.Body.Bytes(), &created); err != nil { t.Fatal(err) }
	waitForTaskStatus(t, store, created.ID, "succeeded")

	db, err = sql.Open("sqlite", target)
	if err != nil { t.Fatal(err) }
	defer db.Close()
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='task_items'").Scan(&count); err != nil { t.Fatal(err) }
	if count != 0 { t.Fatal("Down task did not remove target table") }
}

func waitForTaskStatus(t *testing.T, store *adminstore.Store, id, expected string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		task, err := store.GetMigrationTask(context.Background(), id)
		if err == nil && task.Status == expected { return }
		if err == nil && (task.Status == "failed" || task.Status == "canceled" || task.Status == "interrupted") {
			t.Fatalf("task ended unexpectedly: %+v", task)
		}
		time.Sleep(10 * time.Millisecond)
	}
	task, _ := store.GetMigrationTask(context.Background(), id)
	t.Fatalf("task did not reach %q: %+v", expected, task)
}

func TestMigrationTaskWorkerPanicIsPersistedAsFailure(t *testing.T) {
	store, err := adminstore.Open(context.Background(), ":memory:", apiTestKey)
	if err != nil { t.Fatal(err) }
	defer store.Close()
	manager := NewMigrationTaskManager(store, func(context.Context, adminstore.MigrationTask) error {
		panic("simulated worker panic")
	})
	task, err := manager.Submit(context.Background(), adminstore.MigrationTask{
		DataSourceID: "source-panic", Action: "up",
	})
	if err != nil { t.Fatal(err) }
	waitForTaskStatus(t, store, task.ID, "failed")
	manager.Close()
	got, err := store.GetMigrationTask(context.Background(), task.ID)
	if err != nil { t.Fatal(err) }
	if !strings.Contains(got.Message, "worker stopped unexpectedly") {
		t.Fatalf("expected safe panic failure message, got %q", got.Message)
	}
	if len(manager.locks) != 0 { t.Fatalf("data source lock entries = %d, want 0", len(manager.locks)) }
}

func TestMigrationTasksForSameDataSourceAreSerializedAndReleaseLock(t *testing.T) {
	store, err := adminstore.Open(context.Background(), ":memory:", apiTestKey)
	if err != nil { t.Fatal(err) }
	defer store.Close()

	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var active atomic.Int32
	var maxActive atomic.Int32
	manager := NewMigrationTaskManager(store, func(ctx context.Context, _ adminstore.MigrationTask) error {
		current := active.Add(1)
		for {
			previous := maxActive.Load()
			if current <= previous || maxActive.CompareAndSwap(previous, current) { break }
		}
		started <- struct{}{}
		defer active.Add(-1)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})

	first, err := manager.Submit(context.Background(), adminstore.MigrationTask{DataSourceID: "same-source", Action: "up"})
	if err != nil { t.Fatal(err) }
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first task did not start")
	}

	second, err := manager.Submit(context.Background(), adminstore.MigrationTask{DataSourceID: "same-source", Action: "up"})
	if err != nil { t.Fatal(err) }
	select {
	case <-started:
		t.Fatal("second task started while the first task held the data source lock")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	for i := 0; i < 1; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("second task did not start after the first task completed")
		}
	}
	waitForTaskStatus(t, store, first.ID, "succeeded")
	waitForTaskStatus(t, store, second.ID, "succeeded")
	manager.Close()

	if got := maxActive.Load(); got != 1 { t.Fatalf("maximum concurrent tasks for one data source = %d, want 1", got) }
	if len(manager.locks) != 0 { t.Fatalf("data source lock entries = %d, want 0 after completion", len(manager.locks)) }
}


func TestCancelRunningAndQueuedMigrationTasks(t *testing.T) {
	store, err := adminstore.Open(context.Background(), ":memory:", apiTestKey)
	if err != nil { t.Fatal(err) }
	defer store.Close()

	started := make(chan struct{}, 2)
	manager := NewMigrationTaskManager(store, func(ctx context.Context, _ adminstore.MigrationTask) error {
		started <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	})
	first, err := manager.Submit(context.Background(), adminstore.MigrationTask{
		DataSourceID: "cancel-source", Action: "up",
	})
	if err != nil { t.Fatal(err) }
	select {
	case <-started:
	case <-time.After(2 * time.Second): t.Fatal("first task did not start")
	}
	second, err := manager.Submit(context.Background(), adminstore.MigrationTask{
		DataSourceID: "cancel-source", Action: "up",
	})
	if err != nil { t.Fatal(err) }

	if err := manager.Cancel(second.ID); err != nil { t.Fatalf("cancel queued task: %v", err) }
	if err := manager.Cancel(first.ID); err != nil { t.Fatalf("cancel running task: %v", err) }
	if err := manager.Close(); err != nil { t.Fatalf("close manager: %v", err) }
	waitForTaskStatus(t, store, first.ID, "canceled")
	waitForTaskStatus(t, store, second.ID, "canceled")
	if len(manager.locks) != 0 { t.Fatalf("data source locks remain after cancellation: %d", len(manager.locks)) }
}

func TestMigrationTaskManagerCloseContextCancelsAndWaits(t *testing.T) {
	store, err := adminstore.Open(context.Background(), ":memory:", apiTestKey)
	if err != nil { t.Fatal(err) }
	defer store.Close()

	started := make(chan struct{})
	manager := NewMigrationTaskManager(store, func(ctx context.Context, _ adminstore.MigrationTask) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	task, err := manager.Submit(context.Background(), adminstore.MigrationTask{
		DataSourceID: "shutdown-source", Action: "up",
	})
	if err != nil { t.Fatal(err) }
	select {
	case <-started:
	case <-time.After(2 * time.Second): t.Fatal("task did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := manager.CloseContext(ctx); err != nil { t.Fatalf("CloseContext: %v", err) }
	waitForTaskStatus(t, store, task.ID, "canceled")
	if len(manager.locks) != 0 { t.Fatalf("data source locks remain after shutdown: %d", len(manager.locks)) }
}

func TestMigrationTaskManagerCloseContextHasBoundedWait(t *testing.T) {
	store, err := adminstore.Open(context.Background(), ":memory:", apiTestKey)
	if err != nil { t.Fatal(err) }
	defer store.Close()

	started := make(chan struct{})
	release := make(chan struct{})
	manager := NewMigrationTaskManager(store, func(ctx context.Context, _ adminstore.MigrationTask) error {
		close(started)
		<-release // Simulate a database driver that does not honor context cancellation.
		return ctx.Err()
	})
	task, err := manager.Submit(context.Background(), adminstore.MigrationTask{
		DataSourceID: "unresponsive-source", Action: "up",
	})
	if err != nil { t.Fatal(err) }
	select {
	case <-started:
	case <-time.After(2 * time.Second): t.Fatal("task did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := manager.CloseContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("CloseContext error = %v, want context deadline exceeded", err)
	}
	close(release)
	if err := manager.Close(); err != nil { t.Fatalf("wait for released worker: %v", err) }
	waitForTaskStatus(t, store, task.ID, "canceled")
}
