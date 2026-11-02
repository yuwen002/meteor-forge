package migration

import (
	"context"
	"fmt"
	"sort"
)

// DefaultMigrator 核心迁移引擎：协调 Loader（本地脚本）与 Driver（数据库记录），
// 计算出待 Up / 待回滚的脚本并按版本顺序驱动状态机流转。
// 引擎不感知具体数据库与租户：租户上下文由调用方通过 ctx 传入，Driver 实现负责路由。
type DefaultMigrator struct {
	loader Loader
	driver Driver
}

// 确保 DefaultMigrator 实现了 Migrator 接口。
var _ Migrator = (*DefaultMigrator)(nil)

func NewDefaultMigrator(loader Loader, driver Driver) *DefaultMigrator {
	return &DefaultMigrator{
		loader: loader,
		driver: driver,
	}
}

// acquireLock 获取 Driver 级迁移锁，多实例并发执行时互斥。
func (m *DefaultMigrator) acquireLock(ctx context.Context) error {
	if err := m.driver.Lock(ctx); err != nil {
		return fmt.Errorf("acquire migration lock failed: %w", err)
	}
	return nil
}

// releaseLock 释放迁移锁。使用独立 context：
// 调用方 ctx 可能已被取消，但锁必须尽力归还（会话级锁的连接回收是最终兜底）。
func (m *DefaultMigrator) releaseLock(_ context.Context) {
	_ = m.driver.Unlock(context.Background())
}

// Up 执行所有未完成的迁移脚本。
// 执行前先对全部本地脚本做校验和防篡改比对，已应用过的脚本内容被修改时返回 ErrChecksumMismatch。
// 整个过程持有 Driver 迁移锁，防止多实例并发迁移。
func (m *DefaultMigrator) Up(ctx context.Context) error {
	if err := m.acquireLock(ctx); err != nil {
		return err
	}
	defer m.releaseLock(ctx)

	// 1. 确保数据库中的迁移历史表存在
	if err := m.driver.EnsureTable(ctx); err != nil {
		return err
	}

	// 2. 加载本地所有脚本
	scripts, err := m.loader.LoadScripts(ctx)
	if err != nil {
		return fmt.Errorf("load scripts failed: %w", err)
	}
	if len(scripts) == 0 {
		return ErrNoPendingMigrations
	}

	// 3. 获取数据库中已执行的记录
	appliedRecords, err := m.driver.GetAppliedVersions(ctx)
	if err != nil {
		return err
	}

	// 转成 map 方便快速查找（version -> 存档哈希）
	appliedMap := make(map[string]string)
	for _, r := range appliedRecords {
		appliedMap[r.Version] = r.Checksum
	}

	// 3.5 防篡改校验：已应用过的脚本若本地内容与库中存档哈希不一致，
	// 立即中止，避免“代码与数据库对不上”的线上事故。
	// 存档哈希为空串的存量记录（旧版工具写入）跳过比对，保持升级兼容。
	for _, s := range scripts {
		if archived, applied := appliedMap[s.Version]; applied && archived != "" {
			if archived != s.Checksum() {
				return fmt.Errorf("%w: version %s (db=%s, local=%s)", ErrChecksumMismatch, s.Version, archived, s.Checksum())
			}
		}
	}

	// 4. 筛选出待执行的脚本
	var pendingScripts []*Script
	for _, s := range scripts {
		if _, applied := appliedMap[s.Version]; !applied {
			pendingScripts = append(pendingScripts, s)
		}
	}

	if len(pendingScripts) == 0 {
		return ErrNoPendingMigrations
	}

	// 5. 依次向上执行
	for _, script := range pendingScripts {
		if err := m.driver.ApplyMigration(ctx, script, false); err != nil {
			return fmt.Errorf("migration up failed at version %s: %w", script.Version, err)
		}
	}

	return nil
}

// Down 回滚最近一次执行的迁移（直接复用单锁批量回滚路径，保持锁行为一致）
func (m *DefaultMigrator) Down(ctx context.Context) error {
	return m.DownSteps(ctx, 1)
}

// DownSteps 自最近版本起连续回滚指定步数。
// 整个过程只持有迁移锁一次（批量原子性）：若逐步加锁，多实例环境下
// 中间步骤可能被其他实例插队抢锁并写入新迁移，破坏连续回滚的原子语义。
// 可回滚记录不足 steps 时，退到空即正常返回（宽松语义，便于运维脚本批量退档）；
// 中途失败则返回错误，已回滚的部分不会重试。
func (m *DefaultMigrator) DownSteps(ctx context.Context, steps int) error {
	if steps <= 0 {
		return fmt.Errorf("%w: down steps must be greater than 0, got %d", ErrInvalidVersion, steps)
	}

	if err := m.acquireLock(ctx); err != nil {
		return err
	}
	defer m.releaseLock(ctx)

	if err := m.driver.EnsureTable(ctx); err != nil {
		return err
	}

	appliedRecords, err := m.driver.GetAppliedVersions(ctx)
	if err != nil {
		return err
	}
	if len(appliedRecords) == 0 {
		return ErrNoPendingMigrations // 没有可回滚的记录
	}

	// 防御性排序：按版本号降序，从最晚执行的开始退（不依赖 Driver 内部排序实现）
	sort.Slice(appliedRecords, func(i, j int) bool {
		return appliedRecords[i].Version > appliedRecords[j].Version
	})

	// 加载本地脚本以获取对应的 DownSQL
	scripts, err := m.loader.LoadScripts(ctx)
	if err != nil {
		return err
	}
	scriptMap := make(map[string]*Script, len(scripts))
	for _, s := range scripts {
		scriptMap[s.Version] = s
	}

	// 记录不足时宽松退档：只回滚实际存在的条数（退到空即返回 nil）
	count := min(steps, len(appliedRecords))

	for i := 0; i < count; i++ {
		version := appliedRecords[i].Version
		script, ok := scriptMap[version]
		if !ok {
			return fmt.Errorf("rollback failed: local script for version %s not found", version)
		}
		if err := m.driver.ApplyMigration(ctx, script, true); err != nil {
			return fmt.Errorf("migration down failed at version %s: %w", version, err)
		}
	}

	return nil
}

// ToVersion 迁移或回滚到指定版本；version 为空串表示升级到最新版本，
// 保留值 "0" 表示回滚全部已应用的迁移（goto 语义：多退少补，精准对齐）。
// 升级方向跳过已应用的版本，回滚方向按版本降序逐个执行 DownSQL。
func (m *DefaultMigrator) ToVersion(ctx context.Context, version string) error {
	if err := m.acquireLock(ctx); err != nil {
		return err
	}
	defer m.releaseLock(ctx)

	if err := m.driver.EnsureTable(ctx); err != nil {
		return err
	}

	scripts, err := m.loader.LoadScripts(ctx)
	if err != nil {
		return fmt.Errorf("load scripts failed: %w", err)
	}
	if len(scripts) == 0 {
		return ErrNoPendingMigrations
	}

	// 定位目标版本在脚本序列中的位置
	targetIdx := len(scripts) - 1 // 空串 => 最新版本
	if version == "0" {
		// 保留哨兵 "0"：目标位置排在首个脚本之前，即回滚全部已应用迁移
		targetIdx = -1
	} else if version != "" {
		targetIdx = -1
		for i, s := range scripts {
			if s.Version == version {
				targetIdx = i
				break
			}
		}
		if targetIdx < 0 {
			return fmt.Errorf("%w: %s", ErrInvalidVersion, version)
		}
	}

	appliedRecords, err := m.driver.GetAppliedVersions(ctx)
	if err != nil {
		return err
	}
	appliedMap := make(map[string]bool)
	for _, r := range appliedRecords {
		appliedMap[r.Version] = true
	}
	// 已应用的最大脚本位置，作为当前状态点
	currentIdx := -1
	for i, s := range scripts {
		if appliedMap[s.Version] {
			currentIdx = i
		}
	}

	applied := 0
	// 升级：从 currentIdx 到 targetIdx 逐个应用未执行的脚本
	for i := currentIdx + 1; i <= targetIdx; i++ {
		if appliedMap[scripts[i].Version] {
			continue
		}
		if err := m.driver.ApplyMigration(ctx, scripts[i], false); err != nil {
			return fmt.Errorf("migration to version %s failed at %s: %w", version, scripts[i].Version, err)
		}
		applied++
	}

	// 回退：从 currentIdx 向下（不含 targetIdx）逐个回滚
	for i := currentIdx; i > targetIdx; i-- {
		if !appliedMap[scripts[i].Version] {
			continue
		}
		if err := m.driver.ApplyMigration(ctx, scripts[i], true); err != nil {
			return fmt.Errorf("migration to version %s failed rollback at %s: %w", version, scripts[i].Version, err)
		}
		applied++
	}

	if applied == 0 {
		return ErrNoPendingMigrations
	}
	return nil
}

// Status 获取当前整体迁移状态（对比本地与数据库）。只读操作，不加迁移锁。
func (m *DefaultMigrator) Status(ctx context.Context) ([]StatusItem, error) {
	if err := m.driver.EnsureTable(ctx); err != nil {
		return nil, err
	}

	scripts, err := m.loader.LoadScripts(ctx)
	if err != nil {
		return nil, err
	}

	appliedRecords, err := m.driver.GetAppliedVersions(ctx)
	if err != nil {
		return nil, err
	}

	appliedMap := make(map[string]bool)
	for _, r := range appliedRecords {
		appliedMap[r.Version] = true
	}

	var statuses []StatusItem
	for _, s := range scripts {
		statuses = append(statuses, StatusItem{
			Version:     s.Version,
			Description: s.Description,
			IsApplied:   appliedMap[s.Version],
		})
	}

	// 保持排序
	sort.Slice(statuses, func(i, j int) bool {
		return statuses[i].Version < statuses[j].Version
	})

	return statuses, nil
}
