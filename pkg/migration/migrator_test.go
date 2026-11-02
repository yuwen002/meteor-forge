package migration

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/fstest"
)

// MockDriver 内存级驱动，用于单元测试状态机
type MockDriver struct {
	records []Record
}

func NewMockDriver() *MockDriver {
	return &MockDriver{}
}

func (m *MockDriver) EnsureTable(ctx context.Context) error { return nil }

func (m *MockDriver) GetAppliedVersions(ctx context.Context) ([]Record, error) {
	return m.records, nil
}

func (m *MockDriver) ApplyMigration(ctx context.Context, script *Script, isRollback bool) error {
	if isRollback {
		// 删除记录
		var newRecords []Record
		for _, r := range m.records {
			if r.Version != script.Version {
				newRecords = append(newRecords, r)
			}
		}
		m.records = newRecords
	} else {
		m.records = append(m.records, Record{Version: script.Version, Name: script.Description, Checksum: script.Checksum()})
	}
	return nil
}

// newTestMigrator 基于虚拟脚本目录构建待测引擎
func newTestMigrator() (*DefaultMigrator, *MockDriver) {
	fsys := fstest.MapFS{
		"001_first.up.sql":    {Data: []byte("SELECT 1;")},
		"001_first.down.sql":  {Data: []byte("SELECT 1;")},
		"002_second.up.sql":   {Data: []byte("SELECT 2;")},
		"002_second.down.sql": {Data: []byte("SELECT 2;")},
	}
	driver := NewMockDriver()
	return NewDefaultMigrator(NewFileSystemLoader(fsys, "."), driver), driver
}

func TestMigratorWorkflow(t *testing.T) {
	migrator, driver := newTestMigrator()
	ctx := context.Background()

	// 1. 执行 Up
	if err := migrator.Up(ctx); err != nil {
		t.Fatalf("migrator up failed: %v", err)
	}
	if len(driver.records) != 2 {
		t.Fatalf("expected 2 applied migrations, got %d", len(driver.records))
	}

	// 2. 测试重复执行 Up（应该返回 ErrNoPendingMigrations）
	if err := migrator.Up(ctx); !errors.Is(err, ErrNoPendingMigrations) {
		t.Errorf("expected ErrNoPendingMigrations, got %v", err)
	}

	// 3. 测试状态查询 (Status)
	statuses, err := migrator.Status(ctx)
	if err != nil {
		t.Fatalf("get status failed: %v", err)
	}
	if !statuses[0].IsApplied || !statuses[1].IsApplied {
		t.Errorf("all migrations should be applied")
	}

	// 4. 测试回滚 (Down)
	if err := migrator.Down(ctx); err != nil {
		t.Fatalf("migrator down failed: %v", err)
	}
	if len(driver.records) != 1 {
		t.Errorf("expected 1 migration remaining after rollback, got %d", len(driver.records))
	}
	if driver.records[0].Version != "001" {
		t.Errorf("expected remaining version 001, got %s", driver.records[0].Version)
	}
}

func TestMigratorToVersion(t *testing.T) {
	migrator, driver := newTestMigrator()
	ctx := context.Background()

	// 1. 回退到指定版本：002 -> 001
	if err := migrator.Up(ctx); err != nil {
		t.Fatalf("migrator up failed: %v", err)
	}
	if err := migrator.ToVersion(ctx, "001"); err != nil {
		t.Fatalf("ToVersion(001) failed: %v", err)
	}
	if len(driver.records) != 1 || driver.records[0].Version != "001" {
		t.Fatalf("expected only 001 applied, got %+v", driver.records)
	}

	// 2. 再升级到指定版本：001 -> 002
	if err := migrator.ToVersion(ctx, "002"); err != nil {
		t.Fatalf("ToVersion(002) failed: %v", err)
	}
	if len(driver.records) != 2 {
		t.Fatalf("expected 2 applied migrations, got %d", len(driver.records))
	}

	// 3. 已是目标版本：应返回 ErrNoPendingMigrations
	if err := migrator.ToVersion(ctx, "002"); !errors.Is(err, ErrNoPendingMigrations) {
		t.Errorf("expected ErrNoPendingMigrations, got %v", err)
	}

	// 4. 不存在的版本：应返回 ErrInvalidVersion
	if err := migrator.ToVersion(ctx, "999"); !errors.Is(err, ErrInvalidVersion) {
		t.Errorf("expected ErrInvalidVersion, got %v", err)
	}
}

// 边界：没有任何已应用记录时 Down 应返回 ErrNoPendingMigrations
func TestMigratorDownWhenNothingApplied(t *testing.T) {
	migrator, _ := newTestMigrator()
	if err := migrator.Down(context.Background()); !errors.Is(err, ErrNoPendingMigrations) {
		t.Errorf("expected ErrNoPendingMigrations, got %v", err)
	}
}

// 边界：库中存在已应用记录但本地无对应脚本（版本漂移），Down 应报错且不破坏记录
func TestMigratorDownMissingLocalScript(t *testing.T) {
	migrator, driver := newTestMigrator()
	driver.records = []Record{{Version: "999", Name: "ghost"}}

	err := migrator.Down(context.Background())
	if err == nil || !strings.Contains(err.Error(), "999") {
		t.Fatalf("expected error mentioning missing version 999, got %v", err)
	}
	// 报错后记录不应被错误删除，需要人工介入
	if len(driver.records) != 1 {
		t.Errorf("records should remain untouched on drift error, got %+v", driver.records)
	}
}

// 防篡改：已应用过的脚本内容被修改后，Up 应中止并返回 ErrChecksumMismatch
func TestMigratorUpDetectsTamperedScript(t *testing.T) {
	migrator, driver := newTestMigrator()
	ctx := context.Background()

	if err := migrator.Up(ctx); err != nil {
		t.Fatalf("initial up failed: %v", err)
	}

	// 模拟旧脚本被篡改：把存档哈希改成错误值（等价于本地文件内容变了而库里存的不是）
	driver.records[0].Checksum = "deadbeef"

	err := migrator.Up(ctx)
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("expected ErrChecksumMismatch, got %v", err)
	}
	if !strings.Contains(err.Error(), "001") {
		t.Errorf("mismatch error should mention the tampered version, got %v", err)
	}
}

// 兼容性：存量记录无校验和（旧版工具写入的空串），Up 不应误报篡改
func TestMigratorUpToleratesLegacyRecordsWithoutChecksum(t *testing.T) {
	migrator, driver := newTestMigrator()
	driver.records = []Record{{Version: "001", Name: "first", Checksum: ""}}

	// 001 视为已应用跳过，仅应用 002；全程无 ErrChecksumMismatch
	if err := migrator.Up(context.Background()); err != nil {
		t.Fatalf("legacy empty-checksum record should be skipped, got: %v", err)
	}
	if len(driver.records) != 2 {
		t.Errorf("expected 2 applied records, got %d", len(driver.records))
	}
}
