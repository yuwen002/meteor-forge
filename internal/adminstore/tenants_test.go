package adminstore

import (
	"context"
	"errors"
	"testing"
	"time"
)

func createTenantTestDataSource(t *testing.T, store *Store, name string) DataSource {
	t.Helper()
	source, err := store.Create(context.Background(), DataSourceInput{
		Name: name, Driver: "sqlite", DSN: ":memory:",
	})
	if err != nil { t.Fatalf("create data source: %v", err) }
	return source
}

func TestTenantCRUDAndCodeNormalization(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	source := createTenantTestDataSource(t, store, "tenant-db")

	created, err := store.CreateTenant(ctx, TenantInput{
		Name: "Acme Workspace", Code: " ACME_01 ", DataSourceID: source.ID,
	})
	if err != nil { t.Fatalf("CreateTenant: %v", err) }
	if created.Code != "acme_01" || created.Status != "active" ||
		created.DataSourceID != source.ID || created.DataSourceName != source.Name ||
		created.Driver != "sqlite" {
		t.Fatalf("unexpected tenant: %+v", created)
	}

	items, err := store.ListTenants(ctx)
	if err != nil || len(items) != 1 { t.Fatalf("ListTenants count=%d err=%v", len(items), err) }
	got, err := store.GetTenant(ctx, created.ID)
	if err != nil || got.ID != created.ID { t.Fatalf("GetTenant=%+v err=%v", got, err) }

	updated, err := store.UpdateTenant(ctx, created.ID, TenantInput{
		Name: "Acme Suspended", Code: "acme_01", DataSourceID: source.ID, Status: "suspended",
	})
	if err != nil { t.Fatalf("UpdateTenant: %v", err) }
	if updated.Status != "suspended" || updated.Name != "Acme Suspended" {
		t.Fatalf("unexpected updated tenant: %+v", updated)
	}
	if err := store.DeleteTenant(ctx, created.ID); err != nil { t.Fatalf("DeleteTenant: %v", err) }
	if _, err := store.GetTenant(ctx, created.ID); !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("GetTenant after delete = %v, want ErrTenantNotFound", err)
	}
}

func TestTenantValidationAndDuplicateCode(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	source := createTenantTestDataSource(t, store, "tenant-validation-db")
	base := TenantInput{Name: "Tenant", Code: "tenant-a", DataSourceID: source.ID}
	if _, err := store.CreateTenant(ctx, base); err != nil { t.Fatal(err) }

	duplicate := base
	duplicate.Code = "TENANT-A"
	if _, err := store.CreateTenant(ctx, duplicate); !errors.Is(err, ErrTenantConflict) {
		t.Fatalf("duplicate code error = %v, want ErrTenantConflict", err)
	}
	invalid := base
	invalid.Code = "tenant/a"
	if _, err := store.CreateTenant(ctx, invalid); !errors.Is(err, ErrTenantInvalidInput) {
		t.Fatalf("invalid code error = %v, want ErrTenantInvalidInput", err)
	}
	missingSource := base
	missingSource.Code = "tenant-b"
	missingSource.DataSourceID = "missing-source"
	if _, err := store.CreateTenant(ctx, missingSource); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing data source error = %v, want ErrNotFound", err)
	}
}

func TestTenantReferencesProtectDataSourceMutation(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	source := createTenantTestDataSource(t, store, "tenant-protected-db")
	tenant, err := store.CreateTenant(ctx, TenantInput{
		Name: "Protected tenant", Code: "protected", DataSourceID: source.ID,
	})
	if err != nil { t.Fatal(err) }

	_, err = store.Update(ctx, source.ID, DataSourceInput{
		Name: "renamed db", Driver: "sqlite", DSN: ":memory:",
	})
	if !errors.Is(err, ErrDataSourceHasTenants) {
		t.Fatalf("data source update error = %v, want ErrDataSourceHasTenants", err)
	}
	if err := store.Delete(ctx, source.ID); !errors.Is(err, ErrDataSourceHasTenants) {
		t.Fatalf("data source delete error = %v, want ErrDataSourceHasTenants", err)
	}
	if err := store.DeleteTenant(ctx, tenant.ID); err != nil { t.Fatal(err) }
	if err := store.Delete(ctx, source.ID); err != nil {
		t.Fatalf("delete data source after tenant removal: %v", err)
	}
}

func TestTenantMutationBlockedDuringActiveMigration(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	source := createTenantTestDataSource(t, store, "tenant-active-migration-db")
	tenant, err := store.CreateTenant(ctx, TenantInput{
		Name: "Active tenant", Code: "active-tenant", DataSourceID: source.ID,
	})
	if err != nil { t.Fatal(err) }
	task, err := store.CreateMigrationTask(ctx, MigrationTask{
		ID: "tenant-active-migration", DataSourceID: source.ID, Action: "up",
	})
	if err != nil { t.Fatal(err) }
	if _, err := store.CreateTenant(ctx, TenantInput{
		Name: "Second tenant", Code: "second-tenant", DataSourceID: source.ID,
	}); !errors.Is(err, ErrTenantActiveMigration) {
		t.Fatalf("tenant create during active migration error = %v, want ErrTenantActiveMigration", err)
	}
	if _, err := store.UpdateTenant(ctx, tenant.ID, TenantInput{
		Name: "Changed", Code: "active-tenant", DataSourceID: source.ID, Status: "suspended",
	}); !errors.Is(err, ErrTenantActiveMigration) {
		t.Fatalf("tenant update error = %v, want ErrTenantActiveMigration", err)
	}
	if err := store.DeleteTenant(ctx, tenant.ID); !errors.Is(err, ErrTenantActiveMigration) {
		t.Fatalf("tenant delete error = %v, want ErrTenantActiveMigration", err)
	}
	finished := time.Now().UTC()
	if err := store.UpdateMigrationTask(ctx, task.ID, "canceled", "canceled", nil, &finished); err != nil { t.Fatal(err) }
	if err := store.DeleteTenant(ctx, tenant.ID); err != nil {
		t.Fatalf("delete tenant after migration completion: %v", err)
	}
}
