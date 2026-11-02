package tenant

import (
	"database/sql"
	"sync"
)

// MemoryManager 基于内存的线程安全多租户连接管理器
type MemoryManager struct {
	mu      sync.RWMutex
	tenants map[string]*sql.DB
}

// 确保 MemoryManager 实现了 Manager 接口。
var _ Manager = (*MemoryManager)(nil)

// NewMemoryManager 创建空的租户连接管理器。
func NewMemoryManager() *MemoryManager {
	return &MemoryManager{
		tenants: make(map[string]*sql.DB),
	}
}

// Register 注册或更新租户的数据库连接
func (m *MemoryManager) Register(tenantID string, db *sql.DB) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 如果该租户之前有旧连接，先 Close（重复注册视为替换）
	if oldDB, exists := m.tenants[tenantID]; exists && oldDB != nil {
		_ = oldDB.Close()
	}

	m.tenants[tenantID] = db
	return nil
}

// GetDB 获取指定租户的数据库连接
func (m *MemoryManager) GetDB(tenantID string) (*sql.DB, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	db, exists := m.tenants[tenantID]
	if !exists || db == nil {
		return nil, ErrDBNotFound
	}
	return db, nil
}

// Close 关闭所有租户的数据库连接池
func (m *MemoryManager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for id, db := range m.tenants {
		if db != nil {
			_ = db.Close()
		}
		delete(m.tenants, id)
	}
	return nil
}
