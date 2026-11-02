// Package clickhouse 提供 migration.Driver 接口的 ClickHouse 实现。
// 本包依赖 pkg/migration（接口定义方向：driver -> migration），仅使用 database/sql 抽象，
// 使用方需自行注册 ClickHouse 的 database/sql 驱动（如 clickhouse-go/v2 的 std 接口）。
//
// 与 MySQL 实现的关键差异：
//  1. ClickHouse 不支持行级事务，ApplyMigration 无法把"执行脚本"与"写版本记录"放在同一事务，
//     采用先执行脚本、成功后立即写记录的补偿策略；失败中断后需人工核对。
//  2. 版本记录表使用 ReplacingMergeTree 并按版本去重查询，DELETE 依赖 lightweight mutation。
package clickhouse

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/yuwen002/meteor-forge/pkg/migration"
)

// Driver 实现 migration.Driver 接口的 ClickHouse 适配器。
type Driver struct {
	db        *sql.DB
	tableName string
}

// 确保 Driver 实现了 migration.Driver 接口。
var _ migration.Driver = (*Driver)(nil)

// NewDriver 创建一个 ClickHouse 迁移驱动实例。
// tableName 默认为 "schema_migrations"。
func NewDriver(db *sql.DB, tableName string) *Driver {
	if tableName == "" {
		tableName = "schema_migrations"
	}
	return &Driver{
		db:        db,
		tableName: tableName,
	}
}

// EnsureTable 确保迁移历史记录表存在（ReplacingMergeTree 引擎，按 applied_at 去重取最新）。
func (d *Driver) EnsureTable(ctx context.Context) error {
	query := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			version String,
			name String,
			applied_at DateTime DEFAULT now()
		) ENGINE = ReplacingMergeTree(applied_at)
		ORDER BY version;
	`, d.tableName)

	_, err := d.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("ensure migration table failed: %w", err)
	}
	return nil
}

// GetAppliedVersions 获取所有已经执行过的迁移记录。
// FINAL 保证 ReplacingMergeTree 后台合并前的读去重；未被回滚完成的版本（is_rolled_back=1）需排除。
func (d *Driver) GetAppliedVersions(ctx context.Context) ([]migration.Record, error) {
	// TODO: 若需要保留回滚历史，可加 is_rolled_back 标记列并在此过滤；
	// 当前实现与 MySQL 版对齐：回滚即删除记录。
	query := fmt.Sprintf(
		"SELECT version, name, applied_at FROM %s FINAL ORDER BY version ASC", d.tableName)
	rows, err := d.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query applied versions failed: %w", err)
	}
	defer rows.Close()

	var records []migration.Record
	for rows.Next() {
		var r migration.Record
		if err := rows.Scan(&r.Version, &r.Name, &r.AppliedAt); err != nil {
			return nil, fmt.Errorf("scan migration record failed: %w", err)
		}
		records = append(records, r)
	}

	return records, rows.Err()
}

// ApplyMigration 执行迁移脚本并维护版本记录。
// 无事务保护：先执行脚本，成功后写/删记录；两步之间失败会留下不一致，需人工介入。
// 注意：DownSQL 必须写成幂等/可回滚的 DDL（ClickHouse 无传统回滚能力）。
func (d *Driver) ApplyMigration(ctx context.Context, script *migration.Script, isRollback bool) error {
	var targetSQL string
	if isRollback {
		targetSQL = script.DownSQL
	} else {
		targetSQL = script.UpSQL
	}

	// 1. 执行迁移脚本
	if targetSQL != "" {
		if _, err := d.db.ExecContext(ctx, targetSQL); err != nil {
			return fmt.Errorf("execute migration sql failed (version %s): %w", script.Version, err)
		}
	}

	// 2. 维护版本记录（ClickHouse 无事务，与上一步非原子）
	if isRollback {
		delQuery := fmt.Sprintf("DELETE FROM %s WHERE version = ?", d.tableName)
		if _, err := d.db.ExecContext(ctx, delQuery, script.Version); err != nil {
			return fmt.Errorf("delete migration record failed (version %s 脚本已回滚但记录残留): %w", script.Version, err)
		}
	} else {
		insQuery := fmt.Sprintf("INSERT INTO %s (version, name, applied_at) VALUES (?, ?, ?)", d.tableName)
		if _, err := d.db.ExecContext(ctx, insQuery, script.Version, script.Description, time.Now()); err != nil {
			return fmt.Errorf("insert migration record failed (version %s 脚本已执行但记录缺失): %w", script.Version, err)
		}
	}

	return nil
}
