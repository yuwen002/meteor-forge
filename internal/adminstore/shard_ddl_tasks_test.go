package adminstore

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestShardDDLTaskPersistenceAndAudit(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, ":memory:", testKey)
	if err != nil { t.Fatal(err) }
	t.Cleanup(func(){ _ = store.Close() })
	source, err := store.Create(ctx, DataSourceInput{Name:"ddl target", Driver:"sqlite", DSN:":memory:"})
	if err != nil { t.Fatal(err) }
	rule, err := store.CreateShardRule(ctx, ShardRuleInput{DataSourceID:source.ID, BaseTable:"orders", ShardType:"month"})
	if err != nil { t.Fatal(err) }
	task, err := store.CreateShardDDLTask(ctx, ShardDDLTask{ShardRuleID:rule.ID, DataSourceID:source.ID, Action:"clone", SourceTable:"orders", TargetTable:"orders_202611", RequestSource:"127.0.0.1"})
	if err != nil { t.Fatal(err) }
	if task.Status != "queued" || task.ID == "" { t.Fatalf("unexpected task: %+v", task) }
	if _, err := store.CreateMigrationTask(ctx, MigrationTask{ID: "migration-conflict", DataSourceID: source.ID, Action: "up"}); !errors.Is(err, ErrActiveShardDDLTasks) {
		t.Fatalf("migration submission during shard DDL error=%v, want ErrActiveShardDDLTasks", err)
	}
	if _, err := store.UpdateShardRule(ctx, rule.ID, ShardRuleInput{DataSourceID: source.ID, BaseTable: "orders", ShardType: "year"}); !errors.Is(err, ErrShardRuleHasActiveDDLTasks) {
		t.Fatalf("updating rule with active DDL task error=%v, want ErrShardRuleHasActiveDDLTasks", err)
	}
	if err := store.DeleteShardRule(ctx, rule.ID); !errors.Is(err, ErrShardRuleHasActiveDDLTasks) {
		t.Fatalf("deleting rule with active DDL task error=%v, want ErrShardRuleHasActiveDDLTasks", err)
	}
	finishedBeforeRun := time.Now().UTC()
	if err := store.UpdateShardDDLTask(ctx, task.ID, "succeeded", "completed", nil, &finishedBeforeRun); !errors.Is(err, ErrInvalidTaskTransition) {
		t.Fatalf("queued to succeeded error=%v, want ErrInvalidTaskTransition", err)
	}
	started := time.Now().UTC()
	if err := store.UpdateShardDDLTask(ctx, task.ID, "running", "running", &started, nil); err != nil { t.Fatal(err) }
	finished := time.Now().UTC()
	if err := store.UpdateShardDDLTask(ctx, task.ID, "succeeded", "completed", nil, &finished); err != nil { t.Fatal(err) }
	if err := store.DeleteShardRule(ctx, rule.ID); err != nil {
		t.Fatalf("delete shard rule after terminal DDL task while retaining task audit: %v", err)
	}
	if err := store.UpdateShardDDLTask(ctx, task.ID, "failed", "rewrite terminal state", nil, &finished); !errors.Is(err, ErrInvalidTaskTransition) {
		t.Fatalf("terminal state rewrite error=%v, want ErrInvalidTaskTransition", err)
	}
	got, err := store.GetShardDDLTask(ctx, task.ID)
	if err != nil || got.Status != "succeeded" { t.Fatalf("GetShardDDLTask() = %+v, %v", got, err) }
	audit, err := store.ListShardDDLAuditEvents(ctx, task.ID)
	if err != nil { t.Fatal(err) }
	if len(audit) != 3 || audit[0].ToStatus != "queued" || audit[1].ToStatus != "running" || audit[2].ToStatus != "succeeded" {
		t.Fatalf("unexpected DDL audit sequence: %+v", audit)
	}
	for _, event := range audit {
		if event.RequestSource != "127.0.0.1" {
			t.Errorf("audit event %q request source = %q, want original source 127.0.0.1", event.ToStatus, event.RequestSource)
		}
	}
	listed, err := store.ListShardDDLTasks(ctx, 20)
	if err != nil || len(listed) != 1 || listed[0].ID != task.ID { t.Fatalf("ListShardDDLTasks() = %+v, %v", listed, err) }
}

func TestRecoverInterruptedShardDDLTasks(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, ":memory:", testKey)
	if err != nil { t.Fatal(err) }
	t.Cleanup(func(){ _ = store.Close() })
	source, err := store.Create(ctx, DataSourceInput{Name:"restart target", Driver:"sqlite", DSN:":memory:"})
	if err != nil { t.Fatal(err) }
	rule, err := store.CreateShardRule(ctx, ShardRuleInput{DataSourceID:source.ID, BaseTable:"events", ShardType:"id_hash", HashShards:4})
	if err != nil { t.Fatal(err) }
	task, err := store.CreateShardDDLTask(ctx, ShardDDLTask{ShardRuleID:rule.ID, DataSourceID:source.ID, Action:"clone", SourceTable:"events", TargetTable:"events_1"})
	if err != nil { t.Fatal(err) }
	if err := store.RecoverInterruptedShardDDLTasks(ctx); err != nil { t.Fatal(err) }
	got, err := store.GetShardDDLTask(ctx, task.ID)
	if err != nil || got.Status != "interrupted" { t.Fatalf("recovered task = %+v, %v", got, err) }
	audit, err := store.ListShardDDLAuditEvents(ctx, task.ID)
	if err != nil || len(audit) != 2 || audit[1].ToStatus != "interrupted" { t.Fatalf("recovery audit = %+v, %v", audit, err) }
}

func TestMigrationTaskBlocksShardDDLTask(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, ":memory:", testKey)
	if err != nil { t.Fatal(err) }
	t.Cleanup(func(){ _ = store.Close() })
	source, err := store.Create(ctx, DataSourceInput{Name:"migration target", Driver:"sqlite", DSN:":memory:"})
	if err != nil { t.Fatal(err) }
	rule, err := store.CreateShardRule(ctx, ShardRuleInput{DataSourceID:source.ID, BaseTable:"logs", ShardType:"month"})
	if err != nil { t.Fatal(err) }
	if _, err := store.CreateMigrationTask(ctx, MigrationTask{ID:"active-migration", DataSourceID:source.ID, Action:"up"}); err != nil { t.Fatal(err) }
	_, err = store.CreateShardDDLTask(ctx, ShardDDLTask{ShardRuleID:rule.ID, DataSourceID:source.ID, Action:"clone", SourceTable:"logs", TargetTable:"logs_202611"})
	if !errors.Is(err, ErrActiveTasks) { t.Fatalf("shard DDL submission during migration error=%v, want ErrActiveTasks", err) }
}

