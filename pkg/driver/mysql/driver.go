// Package mysql 提供 migration.Driver 接口的 MySQL 实现。
// 本包依赖 pkg/migration（接口定义方向：driver -> migration），
// 核心迁移引擎 pkg/migration 不引入任何具体数据库的第三方驱动包。
// 使用方需自行注册数据库驱动：_ "github.com/go-sql-driver/mysql"。
package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/yuwen002/meteor-forge/pkg/migration"
)

// MySQLDriver 实现 migration.Driver 接口，负责在 MySQL 中维护版本记录表并在事务中执行迁移脚本。
type MySQLDriver struct {
	db        *sql.DB
	tableName string
	// lockConn 持有 GET_LOCK 的专用连接。MySQL 会话级锁与连接绑定：
	// 若用连接池的 db 直接执行 GET_LOCK，锁会落在随机连接上且 RELEASE_LOCK
	// 无法命中同一连接，故必须独占 *sql.Conn 直到 Unlock。
	lockConn *sql.Conn
}

// 编译期静态检查：确保 MySQLDriver 完全实现了 migration.Driver 接口
var _ migration.Driver = (*MySQLDriver)(nil)

// NewDriver 创建一个 MySQL 迁移驱动实例。
// tableName 默认为 "schema_migrations"。
func NewDriver(db *sql.DB, tableName string) *MySQLDriver {
	if tableName == "" {
		tableName = "schema_migrations"
	}
	return &MySQLDriver{
		db:        db,
		tableName: tableName,
	}
}

// EnsureTable 确保迁移历史记录表存在
func (d *MySQLDriver) EnsureTable(ctx context.Context) error {
	query := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			version VARCHAR(64) NOT NULL PRIMARY KEY,
			name VARCHAR(255) NOT NULL,
			applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			checksum VARCHAR(64) NOT NULL DEFAULT ''
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
	`, d.tableName)

	_, err := d.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("mysql ensure migration table failed: %w", err)
	}
	return nil
}

// GetAppliedVersions 获取所有已经执行过的迁移记录（按版本升序）
func (d *MySQLDriver) GetAppliedVersions(ctx context.Context) ([]migration.Record, error) {
	query := fmt.Sprintf("SELECT version, name, applied_at, checksum FROM %s ORDER BY version ASC", d.tableName)
	rows, err := d.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("mysql query applied versions failed: %w", err)
	}
	defer rows.Close()

	var records []migration.Record
	for rows.Next() {
		var r migration.Record
		if err := rows.Scan(&r.Version, &r.Name, &r.AppliedAt, &r.Checksum); err != nil {
			return nil, fmt.Errorf("mysql scan migration record failed: %w", err)
		}
		records = append(records, r)
	}

	return records, rows.Err()
}

// ApplyMigration 在事务中执行迁移 SQL，并更新版本记录表
func (d *MySQLDriver) ApplyMigration(ctx context.Context, script *migration.Script, isRollback bool) error {
	tx, err := d.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("mysql begin transaction failed: %w", err)
	}

	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()

	var targetSQL string
	if isRollback {
		targetSQL = script.DownSQL
	} else {
		targetSQL = script.UpSQL
	}

	// 1. 执行 SQL 脚本。
	// 注意：脚本含多条语句时，DSN 需开启 multiStatements=true，否则标准驱动会报错。
	if targetSQL != "" {
		if _, err := tx.ExecContext(ctx, targetSQL); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("mysql execute migration sql failed (version %s): %w", script.Version, err)
		}
	}

	// 2. 更新 schema_migrations 记录
	if isRollback {
		delQuery := fmt.Sprintf("DELETE FROM %s WHERE version = ?", d.tableName)
		if _, err := tx.ExecContext(ctx, delQuery, script.Version); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("mysql delete migration record failed: %w", err)
		}
	} else {
		insQuery := fmt.Sprintf("INSERT INTO %s (version, name, applied_at, checksum) VALUES (?, ?, ?, ?)", d.tableName)
		if _, err := tx.ExecContext(ctx, insQuery, script.Version, script.Description, time.Now(), script.Checksum()); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("mysql insert migration record failed: %w", err)
		}
	}

	return tx.Commit()
}

// lockName 锁名与表名绑定，使同一记录表的多实例互斥、不同表的迁移互不阻塞。
func (d *MySQLDriver) lockName() string {
	return fmt.Sprintf("meteor_forge_lock_%s", d.tableName)
}

// Lock 获取 MySQL 会话级排他锁（GET_LOCK，超时 10 秒）。
// 持锁期间进程崩溃/断连时，MySQL 会自动释放该连接上的锁，无死锁残留风险。
func (d *MySQLDriver) Lock(ctx context.Context) error {
	if d.lockConn != nil {
		return fmt.Errorf("mysql: migration lock already held by this instance")
	}

	conn, err := d.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("mysql acquire dedicated connection failed: %w", err)
	}

	var result int
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, 10)", d.lockName()).Scan(&result); err != nil {
		_ = conn.Close()
		return fmt.Errorf("mysql acquire distributed lock failed: %w", err)
	}
	if result != 1 {
		_ = conn.Close()
		return fmt.Errorf("mysql: another migration holds the lock %q (timeout after 10s)", d.lockName())
	}

	d.lockConn = conn
	return nil
}

// Unlock 释放迁移锁并归还专用连接；未持锁时为空操作（幂等）。
func (d *MySQLDriver) Unlock(ctx context.Context) error {
	if d.lockConn == nil {
		return nil
	}
	var result int
	_ = d.lockConn.QueryRowContext(ctx, "SELECT RELEASE_LOCK(?)", d.lockName()).Scan(&result)
	err := d.lockConn.Close()
	d.lockConn = nil
	return err
}
