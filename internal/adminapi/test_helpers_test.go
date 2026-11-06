package adminapi

import "github.com/yuwen002/meteor-forge/internal/adminstore"

// These constructors are intentionally test-only. Production constructors always
// require a valid administrator session whenever a metadata store is configured.
func newTestServerWithStore(store *adminstore.Store) *Server {
 return newServerWithMigrationDir(store, "migrations", false)
}

func newTestServerWithMigrationDir(store *adminstore.Store, migrationDir string) *Server {
 return newServerWithMigrationDir(store, migrationDir, false)
}
