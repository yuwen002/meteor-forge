package adminapi

import (
	"net/http"
	"testing"
)

func TestRoleAuthorizationRegisteredRouteMatrix(t *testing.T) {
	roles := []string{"viewer", "migration_operator", "shard_operator", "administrator"}
	cases := []struct {
		method string
		path   string
		allow  map[string]bool
	}{
		{http.MethodGet, "/api/v1/auth/session", map[string]bool{"viewer": true, "migration_operator": true, "shard_operator": true}},
		{http.MethodGet, "/api/v1/datasources", map[string]bool{"viewer": true, "migration_operator": true, "shard_operator": true}},
		{http.MethodGet, "/api/v1/datasources/d1/migrations/status", map[string]bool{"viewer": true, "migration_operator": true, "shard_operator": true}},
		{http.MethodGet, "/api/v1/tenants", map[string]bool{"viewer": true, "migration_operator": true, "shard_operator": true}},
		{http.MethodGet, "/api/v1/tenants/tenant-1/audit", map[string]bool{"viewer": true, "migration_operator": true, "shard_operator": true}},
		{http.MethodGet, "/api/v1/tenant-health", map[string]bool{"viewer": true, "migration_operator": true, "shard_operator": true}},
		{http.MethodGet, "/api/v1/tenant-health/settings", map[string]bool{"viewer": true, "migration_operator": true, "shard_operator": true}},
		{http.MethodGet, "/api/v1/tenant-health/summary", map[string]bool{"viewer": true, "migration_operator": true, "shard_operator": true}},
		{http.MethodGet, "/api/v1/tenant-health/scheduler", map[string]bool{"viewer": true, "migration_operator": true, "shard_operator": true}},
		{http.MethodGet, "/api/v1/tenant-health/history", map[string]bool{"viewer": true, "migration_operator": true, "shard_operator": true}},
		{http.MethodPut, "/api/v1/tenant-health/settings", map[string]bool{}},
		{http.MethodPost, "/api/v1/tenants/tenant-1/health-check", map[string]bool{}},
		{http.MethodPost, "/api/v1/tenant-health-checks", map[string]bool{}},
		{http.MethodGet, "/api/v1/tenant-discovery/preview", map[string]bool{}},
		{http.MethodPost, "/api/v1/tenant-discovery/sync", map[string]bool{}},
		{http.MethodGet, "/api/v1/migration-tasks/t1/audit", map[string]bool{"viewer": true, "migration_operator": true, "shard_operator": true}},
		{http.MethodGet, "/api/v1/tenant-migration-batches/b1", map[string]bool{"viewer": true, "migration_operator": true, "shard_operator": true}},
		{http.MethodGet, "/api/v1/shard-rules/r1/tables", map[string]bool{"viewer": true, "migration_operator": true, "shard_operator": true}},
		{http.MethodGet, "/api/v1/shard-ddl-tasks/t1/audit", map[string]bool{"viewer": true, "migration_operator": true, "shard_operator": true}},
		{http.MethodGet, "/api/v1/audit-events", map[string]bool{"viewer": true, "migration_operator": true, "shard_operator": true}},
		{http.MethodGet, "/api/v1/auth/users", map[string]bool{}},
		{http.MethodGet, "/api/v1/auth/security-events", map[string]bool{}},
		{http.MethodPost, "/api/v1/auth/logout", map[string]bool{"viewer": true, "migration_operator": true, "shard_operator": true}},
		{http.MethodPost, "/api/v1/auth/password", map[string]bool{"viewer": true, "migration_operator": true, "shard_operator": true}},
		{http.MethodPost, "/api/v1/auth/users", map[string]bool{}},
		{http.MethodPut, "/api/v1/auth/users/u1/role", map[string]bool{}},
		{http.MethodDelete, "/api/v1/auth/users/u1", map[string]bool{}},
		{http.MethodPost, "/api/v1/datasources", map[string]bool{}},
		{http.MethodPost, "/api/v1/datasources/d1/test", map[string]bool{}},
		{http.MethodPost, "/api/v1/tenants/t1/test", map[string]bool{}},
		{http.MethodPut, "/api/v1/shard-rules/r1", map[string]bool{}},
		{http.MethodDelete, "/api/v1/tenants/t1", map[string]bool{}},
		{http.MethodPost, "/api/v1/datasources/d1/migrations/preview", map[string]bool{"migration_operator": true}},
		{http.MethodPost, "/api/v1/datasources/d1/migrations/tasks", map[string]bool{"migration_operator": true}},
		{http.MethodPost, "/api/v1/tenants/t1/migrations/tasks", map[string]bool{"migration_operator": true}},
		{http.MethodPost, "/api/v1/tenant-migration-batches", map[string]bool{"migration_operator": true}},
		{http.MethodPost, "/api/v1/migration-tasks/t1/cancel", map[string]bool{"migration_operator": true}},
		{http.MethodPost, "/api/v1/shard-rules/r1/preview", map[string]bool{"viewer": true, "shard_operator": true}},
		{http.MethodPost, "/api/v1/shard-rules/r1/tables/exists", map[string]bool{"viewer": true, "shard_operator": true}},
		{http.MethodPost, "/api/v1/shard-rules/r1/clone-preview", map[string]bool{"shard_operator": true}},
		{http.MethodPost, "/api/v1/shard-rules/r1/clone-tasks", map[string]bool{"shard_operator": true}},
		{http.MethodPost, "/api/v1/shard-ddl-tasks/t1/cancel", map[string]bool{"shard_operator": true}},
		{http.MethodGet, "/api/v1/future/unknown-route", map[string]bool{}},
		{http.MethodPost, "/api/v1/future/unknown-route", map[string]bool{}},
	}

	for _, tc := range cases {
		for _, role := range roles {
			want := tc.allow[role]
			if role == "administrator" {
				want = true
			}
			if got := authorizeAdminRole(role, tc.method, tc.path); got != want {
				t.Errorf("%s %s role=%s: got %v, want %v", tc.method, tc.path, role, got, want)
			}
		}
	}
}
