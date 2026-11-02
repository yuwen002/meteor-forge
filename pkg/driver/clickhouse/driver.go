// Package clickhouse 提供 migration.Driver 接口的 ClickHouse 实现。
// 本包依赖 pkg/migration（接口定义方向：driver -> migration），仅使用 database/sql 抽象，
// 使用方需自行注册 ClickHouse 驱动：_ "github.com/ClickHouse/clickhouse-go/v2"。
//
// 与 MySQL 驱动的两点关键差异（源于 ClickHouse 的架构特性）：
//  1. 建表必须显式声明表引擎与 ORDER BY 排序键；
//  2. 不支持标准 ACID 事务，ApplyMigration 直行执行 SQL，
//     并通过 INSERT / ALTER TABLE ... DELETE（Mutation）维护迁移状态记录。
package clickhouse

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/yuwen002/meteor-forge/pkg/migration"
)

type ClickHouseDriver struct {
	db        *sql.DB
	tableName string
	// lockConn 持有 getLock 的专用连接（锁与实例会话绑定，断连自动释放）。
	lockConn *sql.Conn
}

// NewDriver 创建一个 ClickHouse 迁移驱动实例。
// tableName 默认为 "schema_migrations"。
func NewDriver(db *sql.DB, tableName string) *ClickHouseDriver {
	if tableName == "" {
		tableName = "schema_migrations"
	}
	return &ClickHouseDriver{
		db:        db,
		tableName: tableName,
	}
}

// 编译期静态检查：确保 ClickHouseDriver 完全实现了 migration.Driver 接口
var _ migration.Driver = (*ClickHouseDriver)(nil)

// EnsureTable 确保迁移历史记录表存在（ClickHouse 必须指定 MergeTree 与 ORDER BY）
func (d *ClickHouseDriver) EnsureTable(ctx context.Context) error {
	query := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			version String,
			name String,
			applied_at DateTime,
			checksum String DEFAULT ''
		) ENGINE = MergeTree()
		ORDER BY (version);
	`, d.tableName)

	_, err := d.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("clickhouse ensure migration table failed: %w", err)
	}
	return nil
}

// GetAppliedVersions 获取所有已经执行过的迁移记录
func (d *ClickHouseDriver) GetAppliedVersions(ctx context.Context) ([]migration.Record, error) {
	query := fmt.Sprintf("SELECT version, name, applied_at, checksum FROM %s ORDER BY version ASC", d.tableName)
	rows, err := d.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("clickhouse query applied versions failed: %w", err)
	}
	defer rows.Close()

	var records []migration.Record
	for rows.Next() {
		var r migration.Record
		if err := rows.Scan(&r.Version, &r.Name, &r.AppliedAt, &r.Checksum); err != nil {
			return nil, fmt.Errorf("clickhouse scan migration record failed: %w", err)
		}
		records = append(records, r)
	}

	return records, rows.Err()
}

// ApplyMigration 执行迁移 SQL 并更新版本记录（ClickHouse 直行执行，不依赖传统事务）。
//
// 注意：ALTER TABLE ... DELETE 是异步 Mutation（默认 best_effort 之后由后台执行），
// 回滚后立刻 Status 可能仍显示旧记录。生产接入建议在 DSN 或会话中设置
// mutations_sync=2，确保删除完成后再返回。
func (d *ClickHouseDriver) ApplyMigration(ctx context.Context, script *migration.Script, isRollback bool) error {
	var targetSQL string
	if isRollback {
		targetSQL = script.DownSQL
	} else {
		targetSQL = script.UpSQL
	}

	// 1. 执行 SQL 脚本
	if targetSQL != "" {
		if _, err := d.db.ExecContext(ctx, targetSQL); err != nil {
			return fmt.Errorf("clickhouse execute migration sql failed (version %s): %w", script.Version, err)
		}
	}

	// 2. 更新 schema_migrations 记录
	if isRollback {
		// ClickHouse 回滚时通过 Mutation 删除记录
		delQuery := fmt.Sprintf("ALTER TABLE %s DELETE WHERE version = ?", d.tableName)
		if _, err := d.db.ExecContext(ctx, delQuery, script.Version); err != nil {
			return fmt.Errorf("clickhouse delete migration record failed: %w", err)
		}
	} else {
		insQuery := fmt.Sprintf("INSERT INTO %s (version, name, applied_at, checksum) VALUES (?, ?, ?, ?)", d.tableName)
		if _, err := d.db.ExecContext(ctx, insQuery, script.Version, script.Description, time.Now(), script.Checksum()); err != nil {
			return fmt.Errorf("clickhouse insert migration record failed: %w", err)
		}
	}

	return nil
}

// Lock 获取 ClickHouse 实例级命名锁（getLock 函数，超时 10 秒），
// 防止多实例并发迁移。基于专用连接持有，断连时服务端自动释放。
// 注：getLock/releaseLock 需服务端支持（ClickHouse >= 21.x），本路径待真实环境验证。
func (d *ClickHouseDriver) Lock(ctx context.Context) error {
	if d.lockConn != nil {
		return fmt.Errorf("clickhouse: migration lock already held by this instance")
	}

	conn, err := d.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("clickhouse acquire dedicated connection failed: %w", err)
	}

	var result uint8
	lockName := "meteor_forge_lock_" + d.tableName
	if err := conn.QueryRowContext(ctx, "SELECT getLock({name:String}, {timeout:UInt32})", sql.Named("name", lockName), sql.Named("timeout", 10)).Scan(&result); err != nil {
		_ = conn.Close()
		return fmt.Errorf("clickhouse acquire distributed lock failed: %w", err)
	}
	if result != 1 {
		_ = conn.Close()
		return fmt.Errorf("clickhouse: another migration holds the lock %q (timeout after 10s)", lockName)
	}

	d.lockConn = conn
	return nil
}

// Unlock 释放命名锁并归还专用连接；未持锁时为空操作（幂等）。
func (d *ClickHouseDriver) Unlock(ctx context.Context) error {
	if d.lockConn == nil {
		return nil
	}
	lockName := "meteor_forge_lock_" + d.tableName
	var result uint8
	_ = d.lockConn.QueryRowContext(ctx, "SELECT releaseLock({name:String})", sql.Named("name", lockName)).Scan(&result)
	err := d.lockConn.Close()
	d.lockConn = nil
	return err
}
