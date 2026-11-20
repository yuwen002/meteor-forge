package adminstore

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestTenantConnectionHealthPersistsNormalizedLatestResult(t *testing.T) {
	store, err := Open(context.Background(), ":memory:", testKey)
	if err != nil { t.Fatal(err) }
	defer store.Close()
	source, err := store.Create(context.Background(), DataSourceInput{Name:"health", Driver:"sqlite", DSN:":memory:"})
	if err != nil { t.Fatal(err) }
	tenant, err := store.CreateTenant(context.Background(), TenantInput{Name:"Tenant", Code:"tenant", DataSourceID:source.ID})
	if err != nil { t.Fatal(err) }
	now := time.Now().UTC().Truncate(time.Millisecond)
	secret := "postgres://user:password@db.internal/app"
	err = store.RecordTenantConnectionHealth(context.Background(), TenantConnectionHealth{
		TenantID:tenant.ID, Status:"unhealthy", CheckedAt:now, DurationMS:42, Message:secret,
	})
	if err != nil { t.Fatal(err) }
	items, err := store.ListTenantConnectionHealth(context.Background())
	if err != nil { t.Fatal(err) }
	if len(items)!=1 || items[0].TenantID!=tenant.ID || items[0].Status!="unhealthy" {
		t.Fatalf("unexpected health records: %+v",items)
	}
	if items[0].Message!="connection check failed; verify the registered data source" || strings.Contains(items[0].Message,"password") {
		t.Fatalf("unsafe health message: %q",items[0].Message)
	}
	next:=now.Add(time.Minute)
	if err:=store.RecordTenantConnectionHealth(context.Background(),TenantConnectionHealth{
		TenantID:tenant.ID,Status:"healthy",CheckedAt:next,DurationMS:8,Message:"connection successful",
	});err!=nil{t.Fatal(err)}
	items,err=store.ListTenantConnectionHealth(context.Background())
	if err!=nil{t.Fatal(err)}
	if len(items)!=1 || items[0].Status!="healthy" || !items[0].CheckedAt.Equal(next) {
		t.Fatalf("latest health result was not updated: %+v",items)
	}
}

func TestTenantConnectionHealthRequiresValidTenantAndStatus(t *testing.T) {
	store,err:=Open(context.Background(),":memory:",testKey)
	if err!=nil{t.Fatal(err)}
	defer store.Close()
	err=store.RecordTenantConnectionHealth(context.Background(),TenantConnectionHealth{
		TenantID:"missing",Status:"healthy",CheckedAt:time.Now(),Message:"connection successful",
	})
	if err==nil{t.Fatal("expected foreign key or missing tenant error")}
	err=store.RecordTenantConnectionHealth(context.Background(),TenantConnectionHealth{
		TenantID:"tenant",Status:"unknown",CheckedAt:time.Now(),Message:"bad",
	})
	if err==nil{t.Fatal("expected invalid status error")}
}
