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
	records     []Record
	lockCalls   int
	unlockCalls int
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

func (m *MockDriver) Lock(ctx context.Context) error {
	m.lockCalls++
	return nil
}

func (m *MockDriver) Unlock(ctx context.Context) error {
	m.unlockCalls++
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

// 分布式锁：写路径（Up/Down/ToVersion/DownSteps）应成对调用 Lock/Unlock；只读的 Status 不加锁
func TestMigratorAcquiresLock(t *testing.T) {
	migrator, driver := newTestMigrator()
	ctx := context.Background()

	if err := migrator.Up(ctx); err != nil {
		t.Fatalf("up failed: %v", err)
	}
	if driver.lockCalls != 1 || driver.unlockCalls != 1 {
		t.Errorf("expected Up to lock/unlock exactly once, got lock=%d unlock=%d", driver.lockCalls, driver.unlockCalls)
	}

	if _, err := migrator.Status(ctx); err != nil {
		t.Fatalf("status failed: %v", err)
	}
	if driver.lockCalls != 1 {
		t.Errorf("Status is read-only and must not acquire the lock, got lock=%d", driver.lockCalls)
	}

	if err := migrator.Down(ctx); err != nil {
		t.Fatalf("down failed: %v", err)
	}
	if driver.lockCalls != 2 || driver.unlockCalls != 2 {
		t.Errorf("expected Down to lock/unlock as a pair, got lock=%d unlock=%d", driver.lockCalls, driver.unlockCalls)
	}
}

// DownSteps：正常步数回滚、超量宽松退到空、非法步数报错、空库第一步透传哨兵错误
func TestMigratorDownSteps(t *testing.T) {
	migrator, driver := newTestMigrator()
	ctx := context.Background()

	if err := migrator.Up(ctx); err != nil {
		t.Fatalf("up failed: %v", err)
	}

	// 非法步数
	if err := migrator.DownSteps(ctx, 0); !errors.Is(err, ErrInvalidVersion) {
		t.Errorf("expected ErrInvalidVersion for steps=0, got %v", err)
	}

	// 回滚 1 步：002 退掉，剩 001
	if err := migrator.DownSteps(ctx, 1); err != nil {
		t.Fatalf("DownSteps(1) failed: %v", err)
	}
	if len(driver.records) != 1 || driver.records[0].Version != "001" {
		t.Fatalf("expected only 001 remaining, got %+v", driver.records)
	}

	// 超量回滚：宽松退到空即返回 nil
	if err := migrator.DownSteps(ctx, 5); err != nil {
		t.Fatalf("DownSteps(5) should tolerate over-rollback to empty, got: %v", err)
	}
	if len(driver.records) != 0 {
		t.Fatalf("expected empty records, got %+v", driver.records)
	}

	// 空库第一步：透传 ErrNoPendingMigrations
	if err := migrator.DownSteps(ctx, 1); !errors.Is(err, ErrNoPendingMigrations) {
		t.Errorf("expected ErrNoPendingMigrations on empty history, got %v", err)
	}
}

// DownSteps 原子性：无论回滚多少步，迁移锁只加一次（防止步间被其他实例插队）
func TestMigratorDownStepsHoldsSingleLock(t *testing.T) {
	migrator, driver := newTestMigrator()
	ctx := context.Background()

	if err := migrator.Up(ctx); err != nil {
		t.Fatalf("up failed: %v", err)
	}
	driver.lockCalls, driver.unlockCalls = 0, 0

	// 一次回滚全部 2 步：锁/解锁各且仅各 1 次
	if err := migrator.DownSteps(ctx, 2); err != nil {
		t.Fatalf("DownSteps(2) failed: %v", err)
	}
	if len(driver.records) != 0 {
		t.Fatalf("expected both migrations rolled back, got %+v", driver.records)
	}
	if driver.lockCalls != 1 || driver.unlockCalls != 1 {
		t.Errorf("DownSteps must hold a single lock across all steps, got lock=%d unlock=%d", driver.lockCalls, driver.unlockCalls)
	}
}

// goto 语义：保留值 "0" 回滚全部已应用迁移（多退少补中的“全部退”）
func TestMigratorToVersionZeroRollsBackAll(t *testing.T) {
	migrator, driver := newTestMigrator()
	ctx := context.Background()

	if err := migrator.Up(ctx); err != nil {
		t.Fatalf("up failed: %v", err)
	}
	if err := migrator.ToVersion(ctx, "0"); err != nil {
		t.Fatalf(`ToVersion("0") failed: %v`, err)
	}
	if len(driver.records) != 0 {
		t.Fatalf("expected all migrations rolled back, got %+v", driver.records)
	}

	// 空库再退全部：已对齐，返回 ErrNoPendingMigrations
	if err := migrator.ToVersion(ctx, "0"); !errors.Is(err, ErrNoPendingMigrations) {
		t.Errorf("expected ErrNoPendingMigrations on empty history, got %v", err)
	}
}

// goto 语义：目标版本低于当前时回退，高于当前时升级，同一条路径双向对齐
func TestMigratorToVersionBidirectionalAlign(t *testing.T) {
	migrator, driver := newTestMigrator()
	ctx := context.Background()

	// 先退到 001（仅回滚 002）
	if err := migrator.Up(ctx); err != nil {
		t.Fatalf("up failed: %v", err)
	}
	if err := migrator.ToVersion(ctx, "001"); err != nil {
		t.Fatalf(`ToVersion("001") failed: %v`, err)
	}
	if len(driver.records) != 1 || driver.records[0].Version != "001" {
		t.Fatalf("expected only 001 remaining, got %+v", driver.records)
	}

	// 目标 “002” 高于当前：自动升级补齐
	if err := migrator.ToVersion(ctx, "002"); err != nil {
		t.Fatalf(`ToVersion("002") upgrade failed: %v`, err)
	}
	if len(driver.records) != 2 {
		t.Fatalf("expected 2 applied after align up, got %+v", driver.records)
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


// 已应用记录必须在本地存在；Up 不得在版本历史漂移时继续执行。
func TestMigratorUpBlocksMissingAppliedScript(t *testing.T) {
	migrator, driver := newTestMigrator()
	driver.records = []Record{{Version: "999", Name: "missing"}}
	err := migrator.Up(context.Background())
	if err == nil || !strings.Contains(err.Error(), "999") {
		t.Fatalf("expected missing local script error for version 999, got %v", err)
	}
	if len(driver.records) != 1 {
		t.Fatalf("history must remain unchanged, got %+v", driver.records)
	}
}

// Down 在迁移锁内校验已应用脚本 checksum，不能回滚与本地版本不一致的历史。
func TestMigratorDownBlocksTamperedAppliedScript(t *testing.T) {
	migrator, driver := newTestMigrator()
	if err := migrator.Up(context.Background()); err != nil { t.Fatal(err) }
	driver.records[len(driver.records)-1].Checksum = "deadbeef"
	err := migrator.Down(context.Background())
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("expected ErrChecksumMismatch, got %v", err)
	}
	if len(driver.records) != 2 {
		t.Fatalf("history must remain unchanged, got %+v", driver.records)
	}
}

// ToVersion 与 Up/Down 使用相同的已应用历史校验规则。
func TestMigratorToVersionBlocksMissingAppliedScript(t *testing.T) {
	migrator, driver := newTestMigrator()
	driver.records = []Record{{Version: "999", Name: "missing"}}
	err := migrator.ToVersion(context.Background(), "001")
	if err == nil || !strings.Contains(err.Error(), "999") {
		t.Fatalf("expected missing local script error for version 999, got %v", err)
	}
	if len(driver.records) != 1 {
		t.Fatalf("history must remain unchanged, got %+v", driver.records)
	}
}

// 选中回滚的脚本必须包含 Down SQL；在执行任何 DDL 前拒绝不完整脚本。
func TestMigratorDownBlocksEmptyDownSQL(t *testing.T) {
	fsys := fstest.MapFS{"001_only.up.sql": {Data: []byte("SELECT 1;")}}
	script := &Script{Version: "001", Description: "only", UpSQL: "SELECT 1;"}
	driver := NewMockDriver()
	driver.records = []Record{{Version: "001", Name: "only", Checksum: script.Checksum()}}
	migrator := NewDefaultMigrator(NewFileSystemLoader(fsys, "."), driver)
	err := migrator.Down(context.Background())
	if err == nil || !strings.Contains(err.Error(), "down SQL") {
		t.Fatalf("expected empty Down SQL error, got %v", err)
	}
	if len(driver.records) != 1 {
		t.Fatalf("history must remain unchanged, got %+v", driver.records)
	}
}


// 空迁移目录也必须检测数据库中遗留的已应用版本，不能误报“没有待迁移”。
func TestMigratorUpDetectsAllLocalScriptsDeleted(t *testing.T) {
	driver := NewMockDriver()
	driver.records = []Record{{Version: "001", Name: "first"}}
	migrator := NewDefaultMigrator(NewFileSystemLoader(fstest.MapFS{}, "."), driver)
	err := migrator.Up(context.Background())
	if err == nil || !strings.Contains(err.Error(), "001") {
		t.Fatalf("expected missing applied script error, got %v", err)
	}
}

// DownSteps 必须在执行第一条回滚 SQL 前校验整个批次。
func TestMigratorDownStepsPreflightsAllDownSQL(t *testing.T) {
	fsys := fstest.MapFS{
		"001_first.up.sql":  {Data: []byte("SELECT 1;")},
		"002_second.up.sql": {Data: []byte("SELECT 2;")},
		"002_second.down.sql": {Data: []byte("SELECT 2;")},
	}
	first := &Script{Version: "001", UpSQL: "SELECT 1;"}
	second := &Script{Version: "002", UpSQL: "SELECT 2;", DownSQL: "SELECT 2;"}
	driver := NewMockDriver()
	driver.records = []Record{
		{Version: "001", Checksum: first.Checksum()},
		{Version: "002", Checksum: second.Checksum()},
	}
	migrator := NewDefaultMigrator(NewFileSystemLoader(fsys, "."), driver)
	err := migrator.DownSteps(context.Background(), 2)
	if err == nil || !strings.Contains(err.Error(), "001") {
		t.Fatalf("expected missing Down SQL error for 001, got %v", err)
	}
	if len(driver.records) != 2 {
		t.Fatalf("preflight failure must not partially roll back records: %+v", driver.records)
	}
}

// ToVersion 回滚多步时也必须先验证所有 Down SQL，不能部分执行后才失败。
func TestMigratorToVersionPreflightsAllDownSQL(t *testing.T) {
	fsys := fstest.MapFS{
		"001_first.up.sql":  {Data: []byte("SELECT 1;")},
		"002_second.up.sql": {Data: []byte("SELECT 2;")},
		"002_second.down.sql": {Data: []byte("SELECT 2;")},
	}
	first := &Script{Version: "001", UpSQL: "SELECT 1;"}
	second := &Script{Version: "002", UpSQL: "SELECT 2;", DownSQL: "SELECT 2;"}
	driver := NewMockDriver()
	driver.records = []Record{
		{Version: "001", Checksum: first.Checksum()},
		{Version: "002", Checksum: second.Checksum()},
	}
	migrator := NewDefaultMigrator(NewFileSystemLoader(fsys, "."), driver)
	err := migrator.ToVersion(context.Background(), "0")
	if err == nil || !strings.Contains(err.Error(), "001") {
		t.Fatalf("expected missing Down SQL error for 001, got %v", err)
	}
	if len(driver.records) != 2 {
		t.Fatalf("preflight failure must not partially roll back records: %+v", driver.records)
	}
}


func TestMigratorProgressObserverReportsStepBoundaries(t *testing.T) {
	migrator, _ := newTestMigrator()
	var updates []Progress
	engine := NewDefaultMigratorWithObserver(migrator.loader, migrator.driver, func(progress Progress) {
		updates = append(updates, progress)
	})
	if err := engine.Up(context.Background()); err != nil { t.Fatal(err) }
	if len(updates) != 4 {
		t.Fatalf("progress updates = %d, want start/completion for two migrations: %+v", len(updates), updates)
	}
	want := []Progress{
		{CurrentVersion: "001", CompletedSteps: 0, TotalSteps: 2, CurrentAction: "up"},
		{CurrentVersion: "001", CompletedSteps: 1, TotalSteps: 2, CurrentAction: "up"},
		{CurrentVersion: "002", CompletedSteps: 1, TotalSteps: 2, CurrentAction: "up"},
		{CurrentVersion: "002", CompletedSteps: 2, TotalSteps: 2, CurrentAction: "up"},
	}
	for i := range want {
		if updates[i] != want[i] { t.Errorf("progress[%d] = %+v, want %+v", i, updates[i], want[i]) }
	}
}
