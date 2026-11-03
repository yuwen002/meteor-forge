// Package sqlite 提供 migration.Driver 接口的 SQLite 实现。
// 本包依赖 pkg/migration（接口定义方向：driver -> migration），仅使用 database/sql 抽象，
// 使用方需自行注册 SQLite 驱动：
//   - 纯 Go：_ "modernc.org/sqlite"（驱动名 "sqlite"，无需 CGO）
//   - CGO：_ "github.com/mattn/go-sqlite3"（驱动名 "sqlite3"）
//
// 注意：SQLite 的 Exec 是否支持一次执行多条语句取决于具体驱动实现，
// 多语句迁移脚本建议每文件只放一条语句，或确认所选驱动支持后使用。
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/yuwen002/meteor-forge/pkg/migration"
)

type SQLiteDriver struct {
	db        *sql.DB
	tableName string
}

// NewDriver 创建一个 SQLite 迁移驱动实例。
// tableName 默认为 "schema_migrations"。
func NewDriver(db *sql.DB, tableName string) *SQLiteDriver {
	if tableName == "" {
		tableName = "schema_migrations"
	}
	return &SQLiteDriver{db: db, tableName: tableName}
}

// 编译期静态检查：确保 SQLiteDriver 完全实现了 migration.Driver 接口
var _ migration.Driver = (*SQLiteDriver)(nil)

// EnsureTable 确保迁移历史记录表存在
func (d *SQLiteDriver) EnsureTable(ctx context.Context) error {
	query := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			version TEXT PRIMARY KEY NOT NULL,
			name TEXT NOT NULL,
			applied_at DATETIME NOT NULL,
			checksum TEXT NOT NULL DEFAULT ''
		);
	`, d.tableName)
	_, err := d.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("sqlite ensure migration table failed: %w", err)
	}
	return nil
}

// GetAppliedVersions 获取所有已经执行过的迁移记录（按版本升序）
func (d *SQLiteDriver) GetAppliedVersions(ctx context.Context) ([]migration.Record, error) {
	query := fmt.Sprintf("SELECT version, name, applied_at, checksum FROM %s ORDER BY version ASC", d.tableName)
	rows, err := d.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("sqlite query applied versions failed: %w", err)
	}
	defer rows.Close()

	var records []migration.Record
	for rows.Next() {
		var r migration.Record
		if err := rows.Scan(&r.Version, &r.Name, &r.AppliedAt, &r.Checksum); err != nil {
			return nil, fmt.Errorf("sqlite scan migration record failed: %w", err)
		}
		records = append(records, r)
	}
	return records, rows.Err()
}

// ApplyMigration 在事务中执行迁移 SQL，并更新版本记录表。
// SQLite 的 DDL 可以回滚（与 MySQL 不同），事务保障是完整的。
// defer tx.Rollback() 在 Commit 成功后为无害的空操作，作为 panic 场景的安全网。
func (d *SQLiteDriver) ApplyMigration(ctx context.Context, script *migration.Script, isRollback bool) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite begin transaction failed: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	targetSQL := script.UpSQL
	if isRollback {
		targetSQL = script.DownSQL
	}

	// 1. 执行 SQL 脚本
	if targetSQL != "" {
		if _, err := tx.ExecContext(ctx, targetSQL); err != nil {
			return fmt.Errorf("sqlite execute migration sql failed (version %s): %w", script.Version, err)
		}
	}

	// 2. 更新 schema_migrations 记录
	if isRollback {
		delQuery := fmt.Sprintf("DELETE FROM %s WHERE version = ?", d.tableName)
		if _, err := tx.ExecContext(ctx, delQuery, script.Version); err != nil {
			return fmt.Errorf("sqlite delete migration record failed: %w", err)
		}
	} else {
		insQuery := fmt.Sprintf("INSERT INTO %s (version, name, applied_at, checksum) VALUES (?, ?, ?, ?)", d.tableName)
		if _, err := tx.ExecContext(ctx, insQuery, script.Version, script.Description, time.Now(), script.Checksum()); err != nil {
			return fmt.Errorf("sqlite insert migration record failed: %w", err)
		}
	}

	return tx.Commit()
}

// Lock SQLite 为单写者模型：写事务本身由数据库文件锁串行化，
// 同进程内 database/sql + 事务已保证互斥，跨进程冲突会直接报 "database is locked"，
// 无需额外的命名锁，这里为空操作以满足 Driver 接口。
// 使用方建议保持默认单连接写入或开启 WAL 模式降低锁竞争。
func (d *SQLiteDriver) Lock(ctx context.Context) error { return nil }

// Unlock 空操作（见 Lock 说明）。
func (d *SQLiteDriver) Unlock(ctx context.Context) error { return nil }