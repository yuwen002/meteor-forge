package adminstore

import (
	"context"
	"testing"
	"time"
)

func TestMigrationTaskPersistenceAndRecovery(t *testing.T) {
	store := newTestStore(t)
	task, err := store.CreateMigrationTask(context.Background(), MigrationTask{
		ID: "task-1", DataSourceID: "source-1", Action: "up",
	})
	if err != nil { t.Fatalf("CreateMigrationTask: %v", err) }
	if task.Status != "queued" || task.CreatedAt.IsZero() { t.Fatalf("unexpected task: %+v", task) }

	started := time.Now().UTC()
	if err := store.UpdateMigrationTask(context.Background(), task.ID, "running", "migration started", &started, nil); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	got, err := store.GetMigrationTask(context.Background(), task.ID)
	if err != nil { t.Fatalf("GetMigrationTask: %v", err) }
	if got.Status != "running" || got.StartedAt == nil { t.Fatalf("unexpected running task: %+v", got) }

	items, err := store.ListMigrationTasks(context.Background(), 10)
	if err != nil || len(items) != 1 { t.Fatalf("ListMigrationTasks items=%d err=%v", len(items), err) }

	if err := store.RecoverInterruptedTasks(context.Background()); err != nil { t.Fatalf("RecoverInterruptedTasks: %v", err) }
	got, err = store.GetMigrationTask(context.Background(), task.ID)
	if err != nil { t.Fatal(err) }
	if got.Status != "interrupted" || got.FinishedAt == nil { t.Fatalf("expected interrupted task, got %+v", got) }
}
