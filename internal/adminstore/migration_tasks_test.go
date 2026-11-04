package adminstore

import (
	"context"
	"errors"
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

func TestActiveMigrationTasksProtectDataSourceMutation(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	source, err := store.Create(ctx, DataSourceInput{
		Name: "migration target", Driver: "sqlite", DSN: ":memory:",
	})
	if err != nil {
		t.Fatalf("create data source: %v", err)
	}
	task, err := store.CreateMigrationTask(ctx, MigrationTask{
		ID: "active-task", DataSourceID: source.ID, Action: "up",
	})
	if err != nil {
		t.Fatalf("create migration task: %v", err)
	}
	if task.Status != "queued" {
		t.Fatalf("task status = %q, want queued", task.Status)
	}

	_, err = store.Update(ctx, source.ID, DataSourceInput{
		Name: "updated target", Driver: "sqlite", DSN: ":memory:",
	})
	if !errors.Is(err, ErrActiveTasks) {
		t.Fatalf("Update error = %v, want ErrActiveTasks", err)
	}
	if err := store.Delete(ctx, source.ID); !errors.Is(err, ErrActiveTasks) {
		t.Fatalf("Delete error = %v, want ErrActiveTasks", err)
	}

	finished := time.Now().UTC()
	if err := store.UpdateMigrationTask(ctx, task.ID, "succeeded", "completed", nil, &finished); err != nil {
		t.Fatalf("finish migration task: %v", err)
	}
	if _, err := store.Update(ctx, source.ID, DataSourceInput{
		Name: "updated target", Driver: "sqlite", DSN: ":memory:",
	}); err != nil {
		t.Fatalf("Update after task completion: %v", err)
	}
	if err := store.Delete(ctx, source.ID); err != nil {
		t.Fatalf("Delete after task completion: %v", err)
	}
}
