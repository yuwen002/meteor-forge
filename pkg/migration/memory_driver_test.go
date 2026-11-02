package migration

import (
	"context"
	"testing"
)

func TestMemoryDriverApplyAndRollback(t *testing.T) {
	d := NewMemoryDriver()
	ctx := context.Background()
	script := &Script{Version: "001", Description: "init"}

	if err := d.EnsureTable(ctx); err != nil {
		t.Fatalf("EnsureTable failed: %v", err)
	}
	// 初始为空
	records, err := d.GetAppliedVersions(ctx)
	if err != nil || len(records) != 0 {
		t.Fatalf("expected empty records, got %+v (err %v)", records, err)
	}

	// 应用迁移
	if err := d.ApplyMigration(ctx, script, false); err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	records, _ = d.GetAppliedVersions(ctx)
	if len(records) != 1 || records[0].Version != "001" {
		t.Fatalf("expected 1 record 001, got %+v", records)
	}

	// 重复应用同一版本应报错
	if err := d.ApplyMigration(ctx, script, false); err == nil {
		t.Error("expected error when applying an already applied version")
	}

	// 回滚成功，记录移除
	if err := d.ApplyMigration(ctx, script, true); err != nil {
		t.Fatalf("rollback failed: %v", err)
	}
	records, _ = d.GetAppliedVersions(ctx)
	if len(records) != 0 {
		t.Fatalf("expected 0 records after rollback, got %+v", records)
	}

	// 回滚未应用过的版本应报错
	if err := d.ApplyMigration(ctx, script, true); err == nil {
		t.Error("expected error when rolling back a version that was never applied")
	}
}

func TestMemoryDriverReturnsCopy(t *testing.T) {
	d := NewMemoryDriver()
	ctx := context.Background()
	if err := d.ApplyMigration(ctx, &Script{Version: "001"}, false); err != nil {
		t.Fatalf("apply failed: %v", err)
	}

	// 篡改返回结果不应影响驱动内部状态（防止调用方绕过 mutex 数据竞争）
	records, _ := d.GetAppliedVersions(ctx)
	records[0].Version = "hacked"

	fresh, _ := d.GetAppliedVersions(ctx)
	if fresh[0].Version != "001" {
		t.Errorf("GetAppliedVersions 未返回副本，内部状态被外部篡改")
	}
}

func TestMemoryDriverTracksExecuted(t *testing.T) {
	d := NewMemoryDriver()
	ctx := context.Background()
	up := &Script{Version: "001", Description: "init", UpSQL: "CREATE TABLE t;"}
	down := &Script{Version: "001", Description: "init", DownSQL: "DROP TABLE t;"}

	// Executed 为公开字段，仅供观察/调试，不参与状态计算
	if err := d.ApplyMigration(ctx, up, false); err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if err := d.ApplyMigration(ctx, down, true); err != nil {
		t.Fatalf("rollback failed: %v", err)
	}

	if len(d.Executed) != 2 {
		t.Fatalf("expected 2 executed entries, got %d", len(d.Executed))
	}
	if d.Executed[0] != up || d.Executed[1] != down {
		t.Errorf("expected exec log to record [up, down] in order, got %v", d.Executed)
	}
}
