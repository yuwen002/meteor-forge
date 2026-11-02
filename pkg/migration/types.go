// Package migration 是 Schema 版本控制引擎：
// Loader 负责解析迁移脚本来源，Driver 抽象底层数据库操作，Migrator 对外提供 Up/Down/ToVersion 能力。
// 本包不引入任何具体数据库的第三方驱动包；MySQL/ClickHouse 等实现位于 pkg/driver/ 下并依赖本包。
package migration

import (
	"errors"
	"time"
)

var (
	ErrNoPendingMigrations = errors.New("migration: no pending migrations to run")
	ErrInvalidVersion      = errors.New("migration: invalid target version")
)

// Script 表示单个 SQL 迁移文件
type Script struct {
	Version     string // 示例: "20261102001" 或 "000001"
	Description string // 示例: "create_users_table"
	UpSQL       string // 向上迁移执行的 SQL
	DownSQL     string // 回滚时执行的 SQL
	CreatedAt   time.Time
}

// Record 表示迁移历史表中记录的一条数据
type Record struct {
	Version   string
	Name      string
	AppliedAt time.Time
}

// StatusItem 迁移状态对比项
type StatusItem struct {
	Version     string
	Description string
	IsApplied   bool
}
