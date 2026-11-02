package migration

import "context"

// Loader 负责解析 SQL 文件来源
type Loader interface {
	LoadScripts(ctx context.Context) ([]*Script, error)
}

// Driver 抽象底层具体的数据库操作（由 pkg/driver/xxx 实现）
type Driver interface {
	// EnsureTable 确保 schema_migrations 记录表存在
	EnsureTable(ctx context.Context) error
	// GetAppliedVersions 获取所有已执行的迁移版本
	GetAppliedVersions(ctx context.Context) ([]Record, error)
	// ApplyMigration 在事务中执行一条 SQL 并写入迁移记录
	ApplyMigration(ctx context.Context, script *Script, isRollback bool) error
	// Lock 获取迁移排他锁，防止多实例并发迁移时发生写冲突；
	// 获取失败（如其他进程持锁或超时）应返回错误，引擎会中止本次迁移。
	// 实现应基于会话级锁（MySQL GET_LOCK / Postgres advisory lock 等）并持有专用连接，
	// 确保连接意外断开时锁能被数据库自动回收。
	Lock(ctx context.Context) error
	// Unlock 释放迁移锁；未持锁时应为安全的空操作（幂等）。
	Unlock(ctx context.Context) error
}

// Migrator 外部统一调用的 Migration 引擎
type Migrator interface {
	// Up 执行所有未完成的迁移
	Up(ctx context.Context) error
	// Down 回滚最近一次迁移
	Down(ctx context.Context) error
	// DownSteps 自最近版本起连续回滚指定步数；可回滚数量不足 steps 时回滚至空即停止
	DownSteps(ctx context.Context, steps int) error
	// ToVersion 迁移或回滚到指定版本（双向，等价于其他工具的 GotoVersion）
	ToVersion(ctx context.Context, version string) error
	// Status 获取当前迁移状态
	Status(ctx context.Context) ([]StatusItem, error)
}
