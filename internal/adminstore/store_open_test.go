package adminstore

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestOpenAppliesSQLitePragmasToReplacementConnections(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "connection-pragmas.db")
	store, err := Open(ctx, path, testKey)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	// Force database/sql to discard its first physical connection. The next
	// query must receive a fresh connection with the same DSN-level settings.
	store.db.SetConnMaxLifetime(time.Nanosecond)
	time.Sleep(2 * time.Millisecond)

	var foreignKeys, busyTimeout int
	if err := store.db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatalf("read foreign_keys on replacement connection: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("read busy_timeout on replacement connection: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys on replacement connection = %d, want 1", foreignKeys)
	}
	if busyTimeout != 5000 {
		t.Fatalf("busy_timeout on replacement connection = %d, want 5000", busyTimeout)
	}
}

func TestOpenSerializesConcurrentInitializationOfSameDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "concurrent-open.db")

	const starters = 2
	start := make(chan struct{})
	type result struct {
		store *Store
		err   error
	}
	results := make(chan result, starters)
	var ready sync.WaitGroup
	ready.Add(starters)
	for i := 0; i < starters; i++ {
		go func() {
			ready.Done()
			<-start
			store, err := Open(ctx, path, testKey)
			results <- result{store: store, err: err}
		}()
	}
	ready.Wait()
	close(start)

	stores := make([]*Store, 0, starters)
	for i := 0; i < starters; i++ {
		select {
		case got := <-results:
			if got.err != nil {
				for _, opened := range stores {
					_ = opened.Close()
				}
				t.Fatalf("concurrent Open() %d failed: %v", i+1, got.err)
			}
			stores = append(stores, got.store)
		case <-time.After(15 * time.Second):
			for _, opened := range stores {
				_ = opened.Close()
			}
			t.Fatal("concurrent Open() did not finish within 15 seconds")
		}
	}
	for _, opened := range stores {
		defer opened.Close()
	}

	var versionCount, latestVersion int
	if err := stores[0].db.QueryRowContext(ctx,
		"SELECT COUNT(*), COALESCE(MAX(version), 0) FROM admin_schema_migrations").
		Scan(&versionCount, &latestVersion); err != nil {
		t.Fatalf("read initialized schema history: %v", err)
	}
	if versionCount != currentMetadataSchemaVersion-10 {
		t.Fatalf("schema history rows = %d, want %d", versionCount, currentMetadataSchemaVersion-10)
	}
	if latestVersion != currentMetadataSchemaVersion {
		t.Fatalf("latest schema version = %d, want %d", latestVersion, currentMetadataSchemaVersion)
	}
}
