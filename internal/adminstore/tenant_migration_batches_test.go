package adminstore

import (
	"context"
	"testing"
)

func TestTenantMigrationBatchPersistsSubmissionFailuresAndAudit(t *testing.T) {
	store, err := Open(context.Background(), ":memory:", testKey)
	if err != nil { t.Fatal(err) }
	defer store.Close()
	batch := TenantMigrationBatchRecord{ID: "batch-audit-1", Total: 2, Actor: "operator", RequestSource: "127.0.0.1"}
	if err := store.CreateTenantMigrationBatch(context.Background(), batch); err != nil { t.Fatal(err) }
	initial, err := store.GetTenantMigrationBatch(context.Background(), batch.ID)
	if err != nil { t.Fatal(err) }
	if initial.Status != "submitting" || initial.Total != 2 || initial.Actor != "operator" {
		t.Fatalf("unexpected initial batch: %+v", initial)
	}
	failures := []TenantMigrationBatchFailure{{TenantID: "tenant-b", Code: "submission_failed", Message: "unable to queue migration"}}
	if err := store.CompleteTenantMigrationBatch(context.Background(), batch.ID, 1, failures); err != nil { t.Fatal(err) }
	completed, err := store.GetTenantMigrationBatch(context.Background(), batch.ID)
	if err != nil { t.Fatal(err) }
	if completed.Status != "partially_submitted" || completed.Queued != 1 || completed.SubmissionFailed != 1 || len(completed.SubmissionFailures) != 1 {
		t.Fatalf("unexpected completed batch: %+v", completed)
	}
	events, err := store.ListTenantMigrationBatchAuditEvents(context.Background(), batch.ID)
	if err != nil { t.Fatal(err) }
	if len(events) != 2 || events[0].Event != "submission_started" || events[1].Event != "submission_completed" {
		t.Fatalf("unexpected batch audit events: %+v", events)
	}
	if events[1].Actor != "operator" || events[1].RequestSource != "127.0.0.1" {
		t.Fatalf("batch audit identity was not persisted: %+v", events[1])
	}
	if err := store.CompleteTenantMigrationBatch(context.Background(), batch.ID, 1, failures); err != ErrTenantMigrationBatchTransition {
		t.Fatalf("second completion error=%v want %v", err, ErrTenantMigrationBatchTransition)
	}
}

func TestTenantMigrationBatchCompletionRollsBackWhenAuditInsertFails(t *testing.T) {
	store, err := Open(context.Background(), ":memory:", testKey)
	if err != nil { t.Fatal(err) }
	defer store.Close()
	batch := TenantMigrationBatchRecord{ID: "batch-audit-rollback", Total: 1, Actor: "operator"}
	if err := store.CreateTenantMigrationBatch(context.Background(), batch); err != nil { t.Fatal(err) }
	if _, err := store.db.ExecContext(context.Background(), "DROP TABLE admin_tenant_migration_batch_audit_events"); err != nil { t.Fatal(err) }
	err = store.CompleteTenantMigrationBatch(context.Background(), batch.ID, 1, nil)
	if err == nil { t.Fatal("expected audit insert failure") }
	got, getErr := store.GetTenantMigrationBatch(context.Background(), batch.ID)
	if getErr != nil { t.Fatal(getErr) }
	if got.Status != "submitting" || got.Queued != 0 || got.SubmissionFailed != 0 {
		t.Fatalf("batch update escaped failed audit transaction: %+v", got)
	}
}
