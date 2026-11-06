package adminapi

import (
	"context"
	"errors"
	"testing"

	"github.com/yuwen002/meteor-forge/internal/adminstore"
	"github.com/yuwen002/meteor-forge/pkg/tenant"
)

func TestResolveTenantDBUsesSharedPoolAndBlocksSuspendedTenants(t *testing.T) {
	store, err := adminstore.Open(context.Background(), ":memory:", apiTestKey)
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = store.Close() })

	source, err := store.Create(context.Background(), adminstore.DataSourceInput{
		Name: "shared tenant database", Driver: "sqlite", DSN: ":memory:",
	})
	if err != nil { t.Fatal(err) }
	first, err := store.CreateTenant(context.Background(), adminstore.TenantInput{
		Name: "First", Code: "first", DataSourceID: source.ID,
	})
	if err != nil { t.Fatal(err) }
	second, err := store.CreateTenant(context.Background(), adminstore.TenantInput{
		Name: "Second", Code: "second", DataSourceID: source.ID,
	})
	if err != nil { t.Fatal(err) }

	server := newTestServerWithStore(store)
	t.Cleanup(func() { _ = server.Close() })

	firstDB, resolvedFirst, err := server.ResolveTenantDB(tenant.WithTenantID(context.Background(), first.ID))
	if err != nil { t.Fatalf("resolve first tenant: %v", err) }
	if resolvedFirst.ID != first.ID { t.Fatalf("resolved tenant=%q, want %q", resolvedFirst.ID, first.ID) }
	if err := firstDB.Ping(); err != nil { t.Fatalf("ping first tenant pool: %v", err) }

	secondDB, resolvedSecond, err := server.ResolveTenantDB(tenant.WithTenantID(context.Background(), second.ID))
	if err != nil { t.Fatalf("resolve second tenant: %v", err) }
	if resolvedSecond.ID != second.ID { t.Fatalf("resolved tenant=%q, want %q", resolvedSecond.ID, second.ID) }
	if firstDB != secondDB {
		t.Fatal("tenants mapped to the same data source should reuse one connection pool")
	}

	_, err = store.UpdateTenant(context.Background(), second.ID, adminstore.TenantInput{
		Name: second.Name, Code: second.Code, DataSourceID: source.ID, Status: "suspended",
	})
	if err != nil { t.Fatal(err) }
	if _, _, err := server.ResolveTenantDB(tenant.WithTenantID(context.Background(), second.ID)); !errors.Is(err, ErrTenantSuspended) {
		t.Fatalf("suspended tenant resolution error=%v, want ErrTenantSuspended", err)
	}
}

func TestResolveTenantDBRequiresTrustedTenantContext(t *testing.T) {
	store, err := adminstore.Open(context.Background(), ":memory:", apiTestKey)
	if err != nil { t.Fatal(err) }
	defer store.Close()
	server := newTestServerWithStore(store)
	defer server.Close()

	if _, _, err := server.ResolveTenantDB(context.Background()); !errors.Is(err, tenant.ErrTenantNotFound) {
		t.Fatalf("missing tenant context error=%v, want ErrTenantNotFound", err)
	}
	if _, _, err := server.ResolveTenantDB(tenant.WithTenantID(context.Background(), "missing")); !errors.Is(err, adminstore.ErrTenantNotFound) {
		t.Fatalf("unknown tenant error=%v, want ErrTenantNotFound", err)
	}
	if _, _, err := NewServer().ResolveTenantDB(context.Background()); !errors.Is(err, ErrTenantConnectionUnavailable) {
		t.Fatalf("unconfigured resolver error=%v, want ErrTenantConnectionUnavailable", err)
	}
}
