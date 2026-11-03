// Package postgres 提供 migration.Driver 接口的 PostgreSQL 实现。
// 本包依赖 pkg/migration（接口定义方向：driver -> migration），仅使用 database/sql 抽象，
// 使用方需自行注册 Postgres 驱动：
//   - _ "github.com/jackc/pgx/v5/stdlib"（驱动名 "pgx"）
//   - _ "github.com/lib/pq"（驱动名 "postgres"）
//
// 占位符约定：PostgreSQL 预处理语句使用 $1, $2 位置参数（非 MySQL 的 ?）。
package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/yuwen002/meteor-forge/pkg/migration"
)

type PostgresDriver struct {
	db        *sql.DB
	tableName string
	// lockConn 持有 advisory lock 的专用连接（会话级锁与连接绑定，同 MySQL 驱动的考量）。
	lockConn *sql.Conn
}

// NewDriver 创建一个 PostgreSQL 迁移驱动实例。
// tableName 默认为 "schema_migrations"。
func NewDriver(db *sql.DB, tableName string) *PostgresDriver {
	if tableName == "" {
		tableName = "schema_migrations"
	}
	return &PostgresDriver{db: db, tableName: tableName}
}

// 编译期静态检查：确保 PostgresDriver 完全实现了 migration.Driver 接口
var _ migration.Driver = (*PostgresDriver)(nil)

// EnsureTable 确保迁移历史记录表存在
func (d *PostgresDriver) EnsureTable(ctx context.Context) error {
	query := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			version VARCHAR(64) PRIMARY KEY NOT NULL,
			name VARCHAR(255) NOT NULL,
			applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			checksum VARCHAR(64) NOT NULL DEFAULT ''
		);
	`, d.tableName)
	_, err := d.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("postgres ensure migration table failed: %w", err)
	}
	return nil
}

// GetAppliedVersions 获取所有已经执行过的迁移记录（按版本升序）
func (d *PostgresDriver) GetAppliedVersions(ctx context.Context) ([]migration.Record, error) {
	query := fmt.Sprintf("SELECT version, name, applied_at, checksum FROM %s ORDER BY version ASC", d.tableName)
	rows, err := d.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("postgres query applied versions failed: %w", err)
	}
	defer rows.Close()

	var records []migration.Record
	for rows.Next() {
		var r migration.Record
		if err := rows.Scan(&r.Version, &r.Name, &r.AppliedAt, &r.Checksum); err != nil {
			return nil, fmt.Errorf("postgres scan migration record failed: %w", err)
		}
		records = append(records, r)
	}
	return records, rows.Err()
}

// ApplyMigration 在事务中执行迁移 SQL，并更新版本记录表。
// PostgreSQL 的 DDL 是事务性的（与 MySQL 不同），脚本与版本记录的写入具备完整原子性：
// 脚本执行到一半失败时，已执行的部分会随事务一起回滚。
// defer tx.Rollback() 在 Commit 成功后为无害的空操作，作为 panic 场景的安全网。
func (d *PostgresDriver) ApplyMigration(ctx context.Context, script *migration.Script, isRollback bool) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("postgres begin transaction failed: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	targetSQL := script.UpSQL
	if isRollback {
		targetSQL = script.DownSQL
	}

	// 1. 执行 SQL 脚本。
	// 注意：pgx/lib/pq 的 Exec 不支持一次执行多条语句，
	// 多语句迁移脚本需拆分为多个文件，或改用按分号逐条执行。
	if targetSQL != "" {
		if _, err := tx.ExecContext(ctx, targetSQL); err != nil {
			return fmt.Errorf("postgres execute migration sql failed (version %s): %w", script.Version, err)
		}
	}

	// 2. 更新 schema_migrations 记录（Postgres 使用 $1, $2 占位符）
	if isRollback {
		delQuery := fmt.Sprintf("DELETE FROM %s WHERE version = $1", d.tableName)
		if _, err := tx.ExecContext(ctx, delQuery, script.Version); err != nil {
			return fmt.Errorf("postgres delete migration record failed: %w", err)
		}
	} else {
		insQuery := fmt.Sprintf("INSERT INTO %s (version, name, applied_at, checksum) VALUES ($1, $2, $3, $4)", d.tableName)
		if _, err := tx.ExecContext(ctx, insQuery, script.Version, script.Description, time.Now(), script.Checksum()); err != nil {
			return fmt.Errorf("postgres insert migration record failed: %w", err)
		}
	}

	return tx.Commit()
}

// Lock 获取 PostgreSQL 会话级 advisory 排他锁（pg_advisory_lock 阻塞直至获得，
// 由引擎层 context 控制整体超时）；锁键由表名哈希得到，连接断开时自动释放。
func (d *PostgresDriver) Lock(ctx context.Context) error {
	if d.lockConn != nil {
		return fmt.Errorf("postgres: migration lock already held by this instance")
	}

	conn, err := d.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("postgres acquire dedicated connection failed: %w", err)
	}

	// hashtext 为服务端函数，保证不同进程对同一表名得到相同锁键
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock(hashtext($1))", "meteor_forge_lock_"+d.tableName); err != nil {
		_ = conn.Close()
		return fmt.Errorf("postgres acquire advisory lock failed: %w", err)
	}

	d.lockConn = conn
	return nil
}

// Unlock 释放 advisory 锁并归还专用连接；未持锁时为空操作（幂等）。
func (d *PostgresDriver) Unlock(ctx context.Context) error {
	if d.lockConn == nil {
		return nil
	}
	_, err := d.lockConn.ExecContext(ctx, "SELECT pg_advisory_unlock(hashtext($1))", "meteor_forge_lock_"+d.tableName)
	if cerr := d.lockConn.Close(); err == nil {
		err = cerr
	}
	d.lockConn = nil
	return err
}