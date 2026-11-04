package tenant

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// TenantInfo 表示控制平面（Control Plane）数据库中发现的一个租户及其接入信息。
// Driver 为空时调用方应使用全局默认驱动类型（通常由 CLI --driver 指定）。
type TenantInfo struct {
	ID     string // 租户唯一标识
	DSN    string // 该租户数据库的连接串（Driver 对应的 DSN 格式）
	Driver string // 可选，该租户的数据库类型 (mysql/postgres/sqlite/clickhouse)；空串表示使用全局默认
}

// LoadActiveTenants 从主控数据库（Control DB）中动态发现活跃租户，
// 使 SaaS 系统水平扩展时无需在配置文件中硬编码租户列表。
//
// 约定的控制表结构（表名 t_tenants）：
//
//	CREATE TABLE t_tenants (
//	    tenant_id VARCHAR(64) PRIMARY KEY,
//	    dsn       VARCHAR(512) NOT NULL,
//	    status    VARCHAR(16) NOT NULL DEFAULT 'active',
//	    driver    VARCHAR(32)           -- 可选：mysql/postgres/sqlite/clickhouse；NULL 或空串表示用全局默认
//	);
//
// 仅返回 status = 'active' 的租户；挂起/注销的租户自动跳过。
// driver 列为可选：若控制表无 driver 列，返回的 TenantInfo.Driver 为空串，由调用方使用全局默认。
func LoadActiveTenants(ctx context.Context, controlDB *sql.DB) ([]TenantInfo, error) {
	if controlDB == nil {
		return nil, fmt.Errorf("tenant: control database is nil")
	}
	tenants, err := queryTenantsWithDriver(ctx, controlDB)
	if err == nil {
		return tenants, nil
	}
	// Only use the legacy query when the optional driver column is absent.
	// Connection, permission, scan and iteration failures must not be hidden.
	if !isMissingDriverColumn(err) {
		return nil, fmt.Errorf("tenant: query active tenants with driver failed: %w", err)
	}
	return queryTenantsLegacy(ctx, controlDB)
}

func isMissingDriverColumn(err error) bool {
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"no such column: driver",
		"unknown column 'driver'",
		"column \"driver\" does not exist",
		"column driver does not exist",
	} {
		if strings.Contains(message, marker) { return true }
	}
	return false
}

func queryTenantsWithDriver(ctx context.Context, controlDB *sql.DB) ([]TenantInfo, error) {
	rows, err := controlDB.QueryContext(ctx, "SELECT tenant_id, dsn, COALESCE(driver, '') FROM t_tenants WHERE status = 'active' ORDER BY tenant_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tenants []TenantInfo
	for rows.Next() {
		var t TenantInfo
		if err := rows.Scan(&t.ID, &t.DSN, &t.Driver); err != nil {
			return nil, fmt.Errorf("tenant: scan tenant record failed: %w", err)
		}
		t.ID = strings.TrimSpace(t.ID)
		t.Driver = strings.ToLower(strings.TrimSpace(t.Driver))
		if t.ID == "" || strings.TrimSpace(t.DSN) == "" {
			return nil, fmt.Errorf("tenant: found record with empty id or dsn (id=%q)", t.ID)
		}
		if t.Driver != "" && t.Driver != "mysql" && t.Driver != "postgres" && t.Driver != "sqlite" && t.Driver != "clickhouse" {
			return nil, fmt.Errorf("tenant: unsupported driver %q for tenant %q", t.Driver, t.ID)
		}
		tenants = append(tenants, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tenant: iterate tenant rows failed: %w", err)
	}
	return tenants, nil
}

func queryTenantsLegacy(ctx context.Context, controlDB *sql.DB) ([]TenantInfo, error) {
	rows, err := controlDB.QueryContext(ctx, "SELECT tenant_id, dsn FROM t_tenants WHERE status = 'active' ORDER BY tenant_id")
	if err != nil {
		return nil, fmt.Errorf("tenant: query active tenants failed: %w", err)
	}
	defer rows.Close()

	var tenants []TenantInfo
	for rows.Next() {
		var t TenantInfo
		if err := rows.Scan(&t.ID, &t.DSN); err != nil {
			return nil, fmt.Errorf("tenant: scan tenant record failed: %w", err)
		}
		if t.ID == "" || t.DSN == "" {
			return nil, fmt.Errorf("tenant: found record with empty id or dsn (id=%q)", t.ID)
		}
		tenants = append(tenants, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tenant: iterate tenant rows failed: %w", err)
	}
	return tenants, nil
}