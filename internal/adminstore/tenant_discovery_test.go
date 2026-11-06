package adminstore

import (
	"context"
	"testing"
)

func TestCreateDiscoveredTenantsWithAuditIsAtomic(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, ":memory:", testKey)
	if err != nil { t.Fatal(err) }
	defer store.Close()
	source, err := store.Create(ctx, DataSourceInput{Name:"target", Driver:"sqlite", DSN:":memory:"})
	if err != nil { t.Fatal(err) }

	created, err := store.CreateDiscoveredTenantsWithAudit(ctx, []TenantInput{
		{Name:"Tenant A", Code:"tenant-a", DataSourceID:source.ID, Status:"active"},
		{Name:"Tenant B", Code:"tenant-b", DataSourceID:source.ID, Status:"active"},
	}, "admin", "tenant-discovery:test")
	if err != nil { t.Fatal(err) }
	if len(created)!=2 { t.Fatalf("created %d tenants, want 2",len(created)) }
	for _, item := range created {
		events,total,err:=store.ListTenantAuditEvents(ctx,item.ID,20,0)
		if err!=nil { t.Fatal(err) }
		if total!=1 || len(events)!=1 || events[0].Action!="created" || events[0].Actor!="admin" || events[0].RequestSource!="tenant-discovery:test" {
			t.Fatalf("missing discovery audit for %s: total=%d events=%+v",item.ID,total,events)
		}
	}

	_,err=store.CreateDiscoveredTenantsWithAudit(ctx, []TenantInput{
		{Name:"Tenant C",Code:"tenant-c",DataSourceID:source.ID},
		{Name:"Duplicate",Code:"tenant-a",DataSourceID:source.ID},
	},"admin","tenant-discovery:test")
	if err!=ErrTenantConflict { t.Fatalf("duplicate batch error=%v, want ErrTenantConflict",err) }
	items,err:=store.ListTenants(ctx)
	if err!=nil { t.Fatal(err) }
	if len(items)!=2 { t.Fatalf("failed batch partially committed: tenants=%+v",items) }
}

func TestCreateDiscoveredTenantsWithAuditRejectsInvalidBatch(t *testing.T) {
	ctx:=context.Background()
	store,err:=Open(ctx,":memory:",testKey)
	if err!=nil { t.Fatal(err) }
	defer store.Close()
	if _,err:=store.CreateDiscoveredTenantsWithAudit(ctx,nil,"admin",""); err==nil {
		t.Fatal("empty discovery batch should be rejected")
	}
}
