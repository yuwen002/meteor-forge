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
	events, err := store.ListMigrationAuditEvents(context.Background(), task.ID)
	if err != nil { t.Fatalf("ListMigrationAuditEvents: %v", err) }
	if len(events) != 3 || events[2].FromStatus != "running" || events[2].ToStatus != "interrupted" {
		t.Fatalf("expected recovery audit event, got %+v", events)
	}
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

	started := time.Now().UTC()
	if err := store.UpdateMigrationTask(ctx, task.ID, "running", "started", &started, nil); err != nil {
		t.Fatalf("start migration task: %v", err)
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


func TestMigrationTaskStateTransitionsAreEnforced(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	task, err := store.CreateMigrationTask(ctx, MigrationTask{
		ID: "transition-task", DataSourceID: "source-transition", Action: "up",
	})
	if err != nil { t.Fatal(err) }

	finished := time.Now().UTC()
	if err := store.UpdateMigrationTask(ctx, task.ID, "succeeded", "skip running", nil, &finished); !errors.Is(err, ErrInvalidTaskTransition) {
		t.Fatalf("queued -> succeeded error = %v, want ErrInvalidTaskTransition", err)
	}
	if err := store.UpdateMigrationTask(ctx, task.ID, "running", "started", &finished, nil); err != nil {
		t.Fatalf("queued -> running: %v", err)
	}
	if err := store.UpdateMigrationTask(ctx, task.ID, "succeeded", "completed", nil, &finished); err != nil {
		t.Fatalf("running -> succeeded: %v", err)
	}
	if err := store.UpdateMigrationTask(ctx, task.ID, "running", "reopen", &finished, nil); !errors.Is(err, ErrInvalidTaskTransition) {
		t.Fatalf("succeeded -> running error = %v, want ErrInvalidTaskTransition", err)
	}
	if err := store.UpdateMigrationTask(ctx, "missing-task", "running", "missing", &finished, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing task error = %v, want ErrNotFound", err)
	}
}


func TestMigrationAuditEventsRecordTaskLifecycle(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	task, err := store.CreateMigrationTask(ctx, MigrationTask{
		ID: "audit-task", DataSourceID: "audit-source", Action: "up", RequestSource: "127.0.0.1",
	})
	if err != nil { t.Fatalf("create task: %v", err) }
	started := time.Now().UTC()
	if err := store.UpdateMigrationTask(ctx, task.ID, "running", "migration is running", &started, nil); err != nil {
		t.Fatalf("mark task running: %v", err)
	}
	finished := time.Now().UTC()
	if err := store.UpdateMigrationTask(ctx, task.ID, "succeeded", "migration completed successfully", nil, &finished); err != nil {
		t.Fatalf("mark task succeeded: %v", err)
	}
	events, err := store.ListMigrationAuditEvents(ctx, task.ID)
	if err != nil { t.Fatalf("list audit events: %v", err) }
	if len(events) != 3 { t.Fatalf("audit event count = %d, want 3", len(events)) }
	if events[0].ToStatus != "queued" || events[1].FromStatus != "queued" || events[1].ToStatus != "running" ||
		events[2].FromStatus != "running" || events[2].ToStatus != "succeeded" {
		t.Fatalf("unexpected audit lifecycle: %+v", events)
	}
	for _, event := range events {
		if event.RequestSource != "127.0.0.1" { t.Fatalf("request source = %q", event.RequestSource) }
		if event.Actor != "local-api" { t.Fatalf("actor = %q, want local-api", event.Actor) }
	}
}


func TestMigrationTaskProgressIsPersistedOnlyWhileRunning(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	task, err := store.CreateMigrationTask(ctx, MigrationTask{
		ID: "progress-task", DataSourceID: "progress-source", Action: "up",
	})
	if err != nil { t.Fatal(err) }
	started := time.Now().UTC()
	if err := store.UpdateMigrationTask(ctx, task.ID, "running", "running", &started, nil); err != nil { t.Fatal(err) }
	progress := MigrationTaskProgress{
		CurrentVersion: "000002", CompletedSteps: 1, TotalSteps: 3, CurrentAction: "up",
	}
	if err := store.UpdateMigrationTaskProgress(ctx, task.ID, progress); err != nil {
		t.Fatalf("UpdateMigrationTaskProgress: %v", err)
	}
	got, err := store.GetMigrationTask(ctx, task.ID)
	if err != nil { t.Fatal(err) }
	if got.CurrentVersion != "000002" || got.CompletedSteps != 1 || got.TotalSteps != 3 ||
		got.CurrentAction != "up" || got.ProgressUpdatedAt == nil {
		t.Fatalf("unexpected persisted progress: %+v", got)
	}
	finished := time.Now().UTC()
	if err := store.UpdateMigrationTask(ctx, task.ID, "succeeded", "completed", nil, &finished); err != nil { t.Fatal(err) }
	if err := store.UpdateMigrationTaskProgress(ctx, task.ID, MigrationTaskProgress{
		CurrentVersion: "000003", CompletedSteps: 3, TotalSteps: 3, CurrentAction: "up",
	}); !errors.Is(err, ErrInvalidTaskTransition) {
		t.Fatalf("progress update after terminal state = %v, want ErrInvalidTaskTransition", err)
	}
}

func TestMigrationTaskProgressRejectsInvalidCounters(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	task, err := store.CreateMigrationTask(ctx, MigrationTask{
		ID: "invalid-progress-task", DataSourceID: "progress-source", Action: "up",
	})
	if err != nil { t.Fatal(err) }
	started := time.Now().UTC()
	if err := store.UpdateMigrationTask(ctx, task.ID, "running", "running", &started, nil); err != nil { t.Fatal(err) }
	err = store.UpdateMigrationTaskProgress(ctx, task.ID, MigrationTaskProgress{
		CompletedSteps: 4, TotalSteps: 2, CurrentAction: "up",
	})
	if !errors.Is(err, ErrInvalidInput) { t.Fatalf("invalid progress error = %v, want ErrInvalidInput", err) }
}

func TestMigrationRecoveryPreservesAuditActor(t *testing.T) {
 ctx:=context.Background()
 store,err:=Open(ctx,":memory:",testKey);if err!=nil{t.Fatal(err)};defer store.Close()
 source,err:=store.Create(ctx,DataSourceInput{Name:"recovery actor",Driver:"sqlite",DSN:":memory:"});if err!=nil{t.Fatal(err)}
 task,err:=store.CreateMigrationTask(ctx,MigrationTask{ID:"actor-recovery",DataSourceID:source.ID,Action:"up",Actor:"migration-operator",RequestSource:"127.0.0.9"});if err!=nil{t.Fatal(err)}
 if err:=store.RecoverInterruptedTasks(ctx);err!=nil{t.Fatal(err)}
 events,err:=store.ListMigrationAuditEvents(ctx,task.ID);if err!=nil{t.Fatal(err)}
 if len(events)!=2{t.Fatalf("events=%d, want queued and interrupted",len(events))}
 for _,event:=range events{if event.Actor!="migration-operator"{t.Errorf("actor=%q",event.Actor)}}
}
