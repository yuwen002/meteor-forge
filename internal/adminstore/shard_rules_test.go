package adminstore

import (
	"context"
	"errors"
	"testing"
)

func TestShardRuleCRUDAndValidation(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	source, err := store.Create(ctx, DataSourceInput{Name: "sharding target", Driver: "sqlite", DSN: ":memory:"})
	if err != nil { t.Fatal(err) }

	created, err := store.CreateShardRule(ctx, ShardRuleInput{
		DataSourceID: source.ID, BaseTable: "orders", ShardType: "month",
	})
	if err != nil { t.Fatalf("CreateShardRule() error = %v", err) }
	if created.ID == "" || created.DataSourceID != source.ID || created.Driver != "sqlite" ||
		created.ShardType != "month" || created.HashShards != 16 || created.RangeSize != 1000000 {
		t.Fatalf("unexpected shard rule: %+v", created)
	}

	items, err := store.ListShardRules(ctx)
	if err != nil || len(items) != 1 || items[0].ID != created.ID {
		t.Fatalf("ListShardRules() = %+v, %v", items, err)
	}
	got, err := store.GetShardRule(ctx, created.ID)
	if err != nil || got.BaseTable != "orders" { t.Fatalf("GetShardRule() = %+v, %v", got, err) }

	if _, err := store.CreateShardRule(ctx, ShardRuleInput{
		DataSourceID: source.ID, BaseTable: "orders", ShardType: "year",
	}); !errors.Is(err, ErrShardRuleConflict) {
		t.Fatalf("duplicate rule error = %v, want ErrShardRuleConflict", err)
	}
	renamed, err := store.Update(ctx, source.ID, DataSourceInput{Name: "renamed target", Driver: "sqlite", DSN: ":memory:"})
	if err != nil || renamed.Name != "renamed target" {
		t.Fatalf("renaming a referenced data source without changing connection settings = %+v, %v", renamed, err)
	}
	if _, err := store.Update(ctx, source.ID, DataSourceInput{Name: "changed", Driver: "sqlite", DSN: "file:other.db"}); !errors.Is(err, ErrDataSourceHasShardRules) {
		t.Fatalf("changing a referenced data source DSN error = %v, want ErrDataSourceHasShardRules", err)
	}
	if _, err := store.Update(ctx, source.ID, DataSourceInput{Name: "changed", Driver: "postgres", DSN: "postgres://localhost/db"}); !errors.Is(err, ErrDataSourceHasShardRules) {
		t.Fatalf("changing a referenced data source driver error = %v, want ErrDataSourceHasShardRules", err)
	}
	if err := store.Delete(ctx, source.ID); !errors.Is(err, ErrDataSourceHasShardRules) {
		t.Fatalf("deleting a referenced data source error = %v, want ErrDataSourceHasShardRules", err)
	}
	if _, err := store.CreateShardRule(ctx, ShardRuleInput{
		DataSourceID: "missing", BaseTable: "missing", ShardType: "year",
	}); !errors.Is(err, ErrShardRuleDataSourceNotFound) {
		t.Fatalf("missing data source error = %v", err)
	}

	updated, err := store.UpdateShardRule(ctx, created.ID, ShardRuleInput{
		DataSourceID: source.ID, BaseTable: "orders", ShardType: "id_hash", HashShards: 32,
	})
	if err != nil || updated.HashShards != 32 || updated.ShardType != "id_hash" {
		t.Fatalf("UpdateShardRule() = %+v, %v", updated, err)
	}

	if err := store.DeleteShardRule(ctx, created.ID); err != nil { t.Fatal(err) }
	if _, err := store.GetShardRule(ctx, created.ID); !errors.Is(err, ErrShardRuleNotFound) {
		t.Fatalf("GetShardRule() after delete error = %v", err)
	}
	if err := store.Delete(ctx, source.ID); err != nil {
		t.Fatalf("delete data source after removing shard rule: %v", err)
	}
}

func TestShardRuleRejectsUnsafeOrInvalidConfiguration(t *testing.T) {
	store := newTestStore(t)
	source, err := store.Create(context.Background(), DataSourceInput{Name: "target", Driver: "sqlite", DSN: ":memory:"})
	if err != nil { t.Fatal(err) }

	cases := []ShardRuleInput{
		{DataSourceID: source.ID, BaseTable: "orders;DROP TABLE users", ShardType: "month"},
		{DataSourceID: source.ID, BaseTable: "9orders", ShardType: "month"},
		{DataSourceID: source.ID, BaseTable: "orders", ShardType: "unknown"},
		{DataSourceID: source.ID, BaseTable: "orders", ShardType: "id_hash", HashShards: 4097},
		{DataSourceID: source.ID, BaseTable: "orders", ShardType: "id_range", RangeSize: -1},
	}
	for _, input := range cases {
		if _, err := store.CreateShardRule(context.Background(), input); !errors.Is(err, ErrShardRuleInvalidInput) {
			t.Errorf("CreateShardRule(%+v) error = %v, want ErrShardRuleInvalidInput", input, err)
		}
	}
}
