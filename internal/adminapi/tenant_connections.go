package adminapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/yuwen002/meteor-forge/internal/adminstore"
	"github.com/yuwen002/meteor-forge/pkg/tenant"
)

var (
	ErrTenantSuspended = errors.New("tenant is suspended")
	ErrTenantConnectionUnavailable = errors.New("tenant connection resolver is not configured")
)

type tenantConnectionResolver struct {
	resolver tenant.Resolver
	tenants tenantStore
	dataSources dataSourceStore
	pools *PoolManager
}

func newTenantConnectionResolver(
	resolver tenant.Resolver,
	tenants tenantStore,
	dataSources dataSourceStore,
	pools *PoolManager,
) *tenantConnectionResolver {
	return &tenantConnectionResolver{
		resolver: resolver,
		tenants: tenants,
		dataSources: dataSources,
		pools: pools,
	}
}

// ResolveTenantDB resolves a tenant ID already placed in ctx by trusted upstream
// authentication/authorization middleware and returns its shared data source pool.
// Callers must never derive the tenant ID directly from an untrusted header,
// query parameter, or request body. Authentication and tenant authorization are
// not implemented by the management server yet.
func (s *Server) ResolveTenantDB(ctx context.Context) (*sql.DB, adminstore.Tenant, error) {
	if s.tenantConnections == nil {
		return nil, adminstore.Tenant{}, ErrTenantConnectionUnavailable
	}
	return s.tenantConnections.resolve(ctx)
}

func (r *tenantConnectionResolver) resolve(ctx context.Context) (*sql.DB, adminstore.Tenant, error) {
	if r == nil || r.resolver == nil || r.tenants == nil || r.dataSources == nil || r.pools == nil {
		return nil, adminstore.Tenant{}, ErrTenantConnectionUnavailable
	}
	tenantID, err := r.resolver.Resolve(ctx)
	if err != nil {
		return nil, adminstore.Tenant{}, err
	}
	item, err := r.tenants.GetTenant(ctx, tenantID)
	if err != nil {
		return nil, adminstore.Tenant{}, err
	}
	if item.Status != "active" {
		return nil, adminstore.Tenant{}, ErrTenantSuspended
	}

	db, err := r.pools.Get(ctx, item.DataSourceID, func(ctx context.Context, dataSourceID string) (string, string, error) {
		driver, dsn, err := r.dataSources.ConnectionConfig(ctx, dataSourceID)
		if err != nil {
			return "", "", err
		}
		driverName := map[string]string{
			"mysql": "mysql",
			"postgres": "pgx",
			"sqlite": "sqlite",
			"clickhouse": "clickhouse",
		}[driver]
		if driverName == "" {
			return "", "", fmt.Errorf("unsupported tenant data source driver")
		}
		return driverName, dsn, nil
	})
	if err != nil {
		return nil, adminstore.Tenant{}, fmt.Errorf("resolve tenant database: %w", err)
	}
	return db, item, nil
}
