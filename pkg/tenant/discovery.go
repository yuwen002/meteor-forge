package tenant

import (
	"context"
	"database/sql"
	"fmt"
)

// TenantInfo 表示控制平面（Control Plane）数据库中发现的一个租户及其接入信息。
type TenantInfo struct {
	ID  string // 租户唯一标识
	DSN string // 该租户数据库的连接串（MySQL DSN 格式）
}

// LoadActiveTenants 从主控数据库（Control DB）中动态发现活跃租户，
// 使 SaaS 系统水平扩展时无需在配置文件中硬编码租户列表。
//
// 约定的控制表结构（表名 t_tenants）：
//
//	CREATE TABLE t_tenants (
//	    tenant_id VARCHAR(64) PRIMARY KEY,
//	    dsn       VARCHAR(512) NOT NULL,
//	    status    VARCHAR(16) NOT NULL DEFAULT 'active'
//	);
//
// 仅返回 status = 'active' 的租户；挂起/注销的租户自动跳过。
func LoadActiveTenants(ctx context.Context, controlDB *sql.DB) ([]TenantInfo, error) {
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
		// 防御：跳过关键字段为空的脏数据，避免后续以无效 DSN 建连
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
