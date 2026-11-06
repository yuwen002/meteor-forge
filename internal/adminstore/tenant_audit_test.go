package adminstore

import (
	"context"
	"testing"
)

func TestTenantLifecycleAuditPersistsBeforeAfterAndDeletionHistory(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, ":memory:", testKey)
	if err != nil { t.Fatal(err) }
	defer store.Close()
	source, err := store.Create(ctx, DataSourceInput{Name: "tenant audit source", Driver: "sqlite", DSN: ":memory:"})
	if err != nil { t.Fatal(err) }

	created, err := store.CreateTenantWithAudit(ctx, TenantInput{
		Name: "Acme", Code: "ACME", DataSourceID: source.ID, Status: "active",
	}, "admin-user", "127.0.0.1")
	if err != nil { t.Fatal(err) }
	updated, err := store.UpdateTenantWithAudit(ctx, created.ID, TenantInput{
		Name: "Acme paused", Code: "acme", DataSourceID: source.ID, Status: "suspended",
	}, "admin-user", "127.0.0.1")
	if err != nil { t.Fatal(err) }
	if err := store.DeleteTenantWithAudit(ctx, updated.ID, "admin-user", "127.0.0.1"); err != nil { t.Fatal(err) }

	events, total, err := store.ListTenantAuditEvents(ctx, created.ID, 50, 0)
	if err != nil { t.Fatal(err) }
	if total != 3 || len(events) != 3 { t.Fatalf("audit total=%d events=%+v", total, events) }
	if events[0].Action != "deleted" || events[0].Before == nil || events[0].After != nil {
		t.Fatalf("unexpected delete audit snapshot: %+v", events[0])
	}
	if events[1].Action != "updated" || events[1].Before == nil || events[1].After == nil ||
		events[1].Before.Name != "Acme" || events[1].After.Name != "Acme paused" {
		t.Fatalf("unexpected update audit snapshots: %+v", events[1])
	}
	if events[2].Action != "created" || events[2].Before != nil || events[2].After == nil ||
		events[2].After.Code != "acme" {
		t.Fatalf("unexpected create audit snapshot: %+v", events[2])
	}
	for _, event := range events {
		if event.Actor != "admin-user" || event.RequestSource != "127.0.0.1" {
			t.Fatalf("tenant audit identity missing: %+v", event)
		}
	}
}

func TestTenantLifecycleMutationRollsBackWhenAuditWriteFails(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, ":memory:", testKey)
	if err != nil { t.Fatal(err) }
	defer store.Close()
	source, err := store.Create(ctx, DataSourceInput{Name: "tenant audit rollback source", Driver: "sqlite", DSN: ":memory:"})
	if err != nil { t.Fatal(err) }
	tenant, err := store.CreateTenant(ctx, TenantInput{
		Name: "Rollback tenant", Code: "rollback", DataSourceID: source.ID, Status: "active",
	})
	if err != nil { t.Fatal(err) }
	if _, err := store.db.ExecContext(ctx, "DROP TABLE admin_tenant_audit_events"); err != nil { t.Fatal(err) }
	if _, err := store.CreateTenantWithAudit(ctx, TenantInput{
		Name: "Create must rollback", Code: "create-rollback", DataSourceID: source.ID, Status: "active",
	}, "admin-user", "127.0.0.1"); err == nil {
		t.Fatal("expected tenant create audit failure")
	}
	beforeUpdate, err := store.ListTenants(ctx)
	if err != nil { t.Fatal(err) }
	if len(beforeUpdate) != 1 { t.Fatalf("tenant create escaped failed audit transaction: %+v", beforeUpdate) }

	if _, err := store.UpdateTenantWithAudit(ctx, tenant.ID, TenantInput{
		Name: "Must not persist", Code: "rollback", DataSourceID: source.ID, Status: "suspended",
	}, "admin-user", "127.0.0.1"); err == nil {
		t.Fatal("expected tenant update audit failure")
	}
	got, err := store.GetTenant(ctx, tenant.ID)
	if err != nil { t.Fatal(err) }
	if got.Name != "Rollback tenant" || got.Status != "active" {
		t.Fatalf("tenant update escaped failed audit transaction: %+v", got)
	}
	if err := store.DeleteTenantWithAudit(ctx, tenant.ID, "admin-user", "127.0.0.1"); err == nil {
		t.Fatal("expected tenant delete audit failure")
	}
	if _, err := store.GetTenant(ctx, tenant.ID); err != nil {
		t.Fatalf("tenant delete escaped failed audit transaction: %v", err)
	}
}
