// Package tenant 提供多租户上下文传递、租户解析（Resolver）与数据库连接池路由（Manager）。
package tenant

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

var (
	ErrTenantNotFound = errors.New("tenant: tenant id not found in context")
	ErrDBNotFound     = errors.New("tenant: database instance not found for tenant")
)

type contextKey struct{}

// Resolver 负责从 Context（如 HTTP Request/GRPC 拦截器）中解析出 tenant_id
type Resolver interface {
	Resolve(ctx context.Context) (string, error)
}

// Manager 负责管理多租户的数据库连接映射
type Manager interface {
	// Register 动态注册某个租户的数据库连接
	Register(tenantID string, db *sql.DB) error
	// GetDB 根据 tenantID 获取对应的数据库连接
	GetDB(tenantID string) (*sql.DB, error)
	// Close 释放所有租户连接
	Close() error
}

// WithTenantID 将 tenant_id 写入 Context
func WithTenantID(ctx context.Context, tenantID string) context.Context {
	return context.WithValue(ctx, contextKey{}, strings.TrimSpace(tenantID))
}

// FromContext 从 Context 中提取 tenant_id
func FromContext(ctx context.Context) (string, error) {
	if ctx == nil { return "", ErrTenantNotFound }
	tenantID, ok := ctx.Value(contextKey{}).(string)
	tenantID = strings.TrimSpace(tenantID)
	if !ok || tenantID == "" {
		return "", ErrTenantNotFound
	}
	return tenantID, nil
}
