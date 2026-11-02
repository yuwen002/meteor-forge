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

// Up 执行所有未完成的迁移脚本
func (m *DefaultMigrator) Up(ctx context.Context) error {
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

	// 转成 map 方便快速查找
	appliedMap := make(map[string]bool)
	for _, r := range appliedRecords {
		appliedMap[r.Version] = true
	}

	// 4. 筛选出待执行的脚本
	var pendingScripts []*Script
	for _, s := range scripts {
		if !appliedMap[s.Version] {
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

// Down 回滚最近一次执行的迁移
func (m *DefaultMigrator) Down(ctx context.Context) error {
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

	// 防御性排序，取版本号最大的记录（不依赖 Driver 内部排序实现）
	sort.Slice(appliedRecords, func(i, j int) bool {
		return appliedRecords[i].Version < appliedRecords[j].Version
	})
	lastRecord := appliedRecords[len(appliedRecords)-1]

	// 加载本地脚本以获取对应的 DownSQL
	scripts, err := m.loader.LoadScripts(ctx)
	if err != nil {
		return err
	}

	var targetScript *Script
	for _, s := range scripts {
		if s.Version == lastRecord.Version {
			targetScript = s
			break
		}
	}

	if targetScript == nil {
		return fmt.Errorf("rollback failed: local script for version %s not found", lastRecord.Version)
	}

	// 执行回滚
	if err := m.driver.ApplyMigration(ctx, targetScript, true); err != nil {
		return fmt.Errorf("migration down failed at version %s: %w", targetScript.Version, err)
	}

	return nil
}

// ToVersion 迁移或回滚到指定版本；version 为空串表示升级到最新版本。
// 升级方向跳过已应用的版本，回滚方向按版本降序逐个执行 DownSQL。
func (m *DefaultMigrator) ToVersion(ctx context.Context, version string) error {
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
	if version != "" {
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

// Status 获取当前整体迁移状态（对比本地与数据库）
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
