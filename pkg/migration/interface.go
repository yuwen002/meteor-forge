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
}

// Migrator 外部统一调用的 Migration 引擎
type Migrator interface {
	// Up 执行所有未完成的迁移
	Up(ctx context.Context) error
	// Down 回滚最近一次迁移
	Down(ctx context.Context) error
	// ToVersion 迁移或回滚到指定版本
	ToVersion(ctx context.Context, version string) error
	// Status 获取当前迁移状态
	Status(ctx context.Context) ([]StatusItem, error)
}
