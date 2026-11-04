package tenant

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
)

var (
	ErrInvalidTenantID = errors.New("tenant: tenant id must not be empty")
	ErrNilTenantDB = errors.New("tenant: database handle must not be nil")
	ErrManagerClosed = errors.New("tenant: connection manager is closed")
)

// MemoryManager 基于内存的线程安全多租户连接管理器。
// 它只管理连接句柄，不拥有租户元数据或凭据。
type MemoryManager struct {
	mu      sync.RWMutex
	tenants map[string]*sql.DB
	closed  bool
}

// 确保 MemoryManager 实现了 Manager 接口。
var _ Manager = (*MemoryManager)(nil)

// NewMemoryManager 创建空的租户连接管理器。
func NewMemoryManager() *MemoryManager {
	return &MemoryManager{tenants: make(map[string]*sql.DB)}
}

// Register 注册或替换租户数据库连接。相同连接重复注册不会关闭当前连接。
func (m *MemoryManager) Register(tenantID string, db *sql.DB) error {
	if strings.TrimSpace(tenantID) == "" {
		return ErrInvalidTenantID
	}
	if db == nil {
		return ErrNilTenantDB
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrManagerClosed
	}
	oldDB := m.tenants[tenantID]
	m.tenants[tenantID] = db
	m.mu.Unlock()

	if oldDB != nil && oldDB != db {
		_ = oldDB.Close()
	}
	return nil
}

// GetDB 获取指定租户的数据库连接池。
func (m *MemoryManager) GetDB(tenantID string) (*sql.DB, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, ErrInvalidTenantID
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return nil, ErrManagerClosed
	}
	db, exists := m.tenants[tenantID]
	if !exists || db == nil {
		return nil, ErrDBNotFound
	}
	return db, nil
}

// Close 关闭所有租户连接池。重复调用安全；连接池在释放锁后关闭，
// 避免慢速驱动 Close 阻塞其他管理器状态查询。
func (m *MemoryManager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	tenants := m.tenants
	m.tenants = make(map[string]*sql.DB)
	m.mu.Unlock()

	var closeErrors []error
	for id, db := range tenants {
		if db == nil { continue }
		if err := db.Close(); err != nil {
			closeErrors = append(closeErrors, fmt.Errorf("close tenant %s database: %w", id, err))
		}
	}
	return errors.Join(closeErrors...)
}
