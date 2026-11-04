package adminapi

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"
)

var ErrPoolManagerClosed = errors.New("data source pool manager is closed")

type managedPool struct {
	driver string
	dsn    string
	db     *sql.DB
}

// poolOpener is injectable so lifecycle behavior can be tested without a live database.
type poolOpener func(driver, dsn string) (*sql.DB, error)

// PoolManager caches target database pools by management data source ID.
// The mutex serializes configuration reads, replacement, invalidation, and shutdown.
type PoolManager struct {
	mu     sync.Mutex
	pools  map[string]managedPool
	closed bool
	open   poolOpener
}

func NewPoolManager() *PoolManager {
	return newPoolManager(sql.Open)
}

func newPoolManager(open poolOpener) *PoolManager {
	return &PoolManager{pools: make(map[string]managedPool), open: open}
}

// Get returns a reusable pool. loadConfig must return the current driver and DSN.
// Configuration is loaded while holding the registry lock so invalidation cannot
// race with insertion of a pool built from an obsolete configuration.
func (m *PoolManager) Get(
	ctx context.Context,
	id string,
	loadConfig func(context.Context, string) (driver, dsn string, err error),
) (*sql.DB, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return nil, ErrPoolManagerClosed
	}
	driver, dsn, err := loadConfig(ctx, id)
	if err != nil {
		return nil, err
	}
	if current, ok := m.pools[id]; ok {
		if current.driver == driver && current.dsn == dsn {
			return current.db, nil
		}
		_ = current.db.Close()
		delete(m.pools, id)
	}

	db, err := m.open(driver, dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)
	m.pools[id] = managedPool{driver: driver, dsn: dsn, db: db}
	return db, nil
}

// Invalidate retires a cached pool after its data source is updated or deleted.
// database/sql prevents new operations after Close and allows in-flight operations
// to finish according to the driver's/database/sql lifecycle.
func (m *PoolManager) Invalidate(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if current, ok := m.pools[id]; ok {
		delete(m.pools, id)
		_ = current.db.Close()
	}
}

// InvalidateIfCurrent retires a pool only when it is still the registered instance.
// It prevents a failed Ping on an old in-flight request from closing a newer pool
// that was created after a concurrent data source update.
func (m *PoolManager) InvalidateIfCurrent(id string, db *sql.DB) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if current, ok := m.pools[id]; ok && current.db == db {
		delete(m.pools, id)
		_ = current.db.Close()
	}
}

// Close prevents future acquisitions and closes every registered pool.
func (m *PoolManager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	m.closed = true
	var firstErr error
	for id, current := range m.pools {
		if err := current.db.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		delete(m.pools, id)
	}
	return firstErr
}
