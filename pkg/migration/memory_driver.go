package migration

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// MemoryDriver 内存版 Driver 实现，仅用于单元测试与本地演示，不落库、不执行 SQL。
// 正式的 MySQL / ClickHouse 实现位于 pkg/driver/mysql 与 pkg/driver/clickhouse。
type MemoryDriver struct {
	mu sync.Mutex
	// applied 模拟 schema_migrations 表中已应用的记录。
	applied []Record
	// Executed 记录按顺序执行过的脚本，便于测试断言。
	Executed []*Script
	// 模拟迁移锁：lockHeld 表示当前持锁，LockCalls/UnlockCalls 供测试断言引擎调用次数。
	lockHeld    bool
	LockCalls   int
	UnlockCalls int
}

// 确保 MemoryDriver 实现了 Driver 接口。
var _ Driver = (*MemoryDriver)(nil)

// NewMemoryDriver 创建内存驱动实例。
func NewMemoryDriver() *MemoryDriver {
	return &MemoryDriver{}
}

// EnsureTable 内存实现无需建表，始终返回 nil。
func (d *MemoryDriver) EnsureTable(ctx context.Context) error { return nil }

// GetAppliedVersions 返回已应用的迁移记录副本。
func (d *MemoryDriver) GetAppliedVersions(ctx context.Context) ([]Record, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]Record, len(d.applied))
	copy(out, d.applied)
	return out, nil
}

// ApplyMigration 模拟执行迁移脚本：升级时追加记录，回滚时移除记录。
func (d *MemoryDriver) ApplyMigration(ctx context.Context, script *Script, isRollback bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if isRollback {
		for i, r := range d.applied {
			if r.Version == script.Version {
				d.applied = append(d.applied[:i], d.applied[i+1:]...)
				d.Executed = append(d.Executed, script)
				return nil
			}
		}
		return fmt.Errorf("migration/memory: 回滚的版本 %s 未处于已应用状态", script.Version)
	}

	for _, r := range d.applied {
		if r.Version == script.Version {
			return fmt.Errorf("migration/memory: 版本 %s 已被应用", script.Version)
		}
	}
	d.applied = append(d.applied, Record{
		Version:   script.Version,
		Name:      script.Description,
		AppliedAt: time.Now(),
		Checksum:  script.Checksum(),
	})
	d.Executed = append(d.Executed, script)
	return nil
}

// Lock 模拟获取迁移锁；重复加锁返回错误，与真实数据库排他锁语义一致。
func (d *MemoryDriver) Lock(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.lockHeld {
		return fmt.Errorf("migration/memory: 锁已被持有，不能重复加锁")
	}
	d.lockHeld = true
	d.LockCalls++
	return nil
}

// Unlock 模拟释放迁移锁；未持锁时为空操作（幂等）。
func (d *MemoryDriver) Unlock(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.lockHeld {
		return nil
	}
	d.lockHeld = false
	d.UnlockCalls++
	return nil
}
