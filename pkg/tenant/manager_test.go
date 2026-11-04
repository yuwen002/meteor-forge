package tenant

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"
	"testing"
)

// fakeDriver 永不建立真实连接（sql.Open 是懒连接，Open 只在首次使用时被调用）。
// 仅用于在测试中获得真实可用的 *sql.DB 句柄，验证 Manager 的路由行为。
type fakeDriver struct{}

func (fakeDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("tenant test: fake driver 不支持真实连接")
}

var fakeRegisterOnce sync.Once

// newTestDB 返回一个绑定 fakeDriver 的 *sql.DB，不会触碰任何数据库。
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	fakeRegisterOnce.Do(func() {
		sql.Register("tenant-test-fake", fakeDriver{})
	})
	db, err := sql.Open("tenant-test-fake", "fake-dsn")
	if err != nil {
		t.Fatalf("sql.Open failed: %v", err)
	}
	// 幂等清理：即使 Manager.Close 已关闭过，重复 Close 也安全
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestMemoryManagerRegisterAndGet(t *testing.T) {
	m := NewMemoryManager()
	db := newTestDB(t)

	if err := m.Register("t1", db); err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	got, err := m.GetDB("t1")
	if err != nil {
		t.Fatalf("GetDB failed: %v", err)
	}
	if got != db {
		t.Errorf("GetDB 应返回注册时的同一连接实例")
	}

	// 未注册租户返回 ErrDBNotFound
	if _, err := m.GetDB("missing"); !errors.Is(err, ErrDBNotFound) {
		t.Errorf("expected ErrDBNotFound, got %v", err)
	}
}

func TestMemoryManagerRegisterReplaces(t *testing.T) {
	m := NewMemoryManager()
	db1, db2 := newTestDB(t), newTestDB(t)

	if err := m.Register("t1", db1); err != nil {
		t.Fatalf("first Register failed: %v", err)
	}
	// 重复注册视为替换（MemoryManager 语义），旧连接会被 Close
	if err := m.Register("t1", db2); err != nil {
		t.Fatalf("replace Register failed: %v", err)
	}
	got, err := m.GetDB("t1")
	if err != nil {
		t.Fatalf("GetDB failed: %v", err)
	}
	if got != db2 {
		t.Errorf("重复注册后应返回新连接")
	}
}

func TestMemoryManagerClose(t *testing.T) {
	m := NewMemoryManager()
	if err := m.Register("t1", newTestDB(t)); err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	// Close 后映射清空
	if _, err := m.GetDB("t1"); !errors.Is(err, ErrDBNotFound) {
		t.Errorf("expected ErrDBNotFound after Close, got %v", err)
	}
}

func TestMemoryManagerConcurrent(t *testing.T) {
	m := NewMemoryManager()
	const n = 50

	// 在主 goroutine 预先创建好连接与租户 ID，避免在子 goroutine 中调用 t.Fatalf

	type pair struct {
		id string
		db *sql.DB
	}
	pairs := make([]pair, n)
	for i := range n {
		pairs[i] = pair{id: string(rune('a'+i%26)) + string(rune('0'+i)), db: newTestDB(t)}
	}

	var wg sync.WaitGroup
	// 并发写入不同租户
	for _, p := range pairs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := m.Register(p.id, p.db); err != nil {
				t.Errorf("concurrent Register failed: %v", err)
			}
		}()
	}
	// 并发读取（多数会命中 ErrDBNotFound，同样是合法路径）
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = m.GetDB(pairs[0].id)
		}()
	}
	wg.Wait()

	if err := m.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}


func TestMemoryManagerValidatesRegistrationAndClosedState(t *testing.T) {
	manager := NewMemoryManager()
	if err := manager.Register("", newTestDB(t)); !errors.Is(err, ErrInvalidTenantID) {
		t.Fatalf("empty tenant id error = %v, want ErrInvalidTenantID", err)
	}
	if err := manager.Register("tenant", nil); !errors.Is(err, ErrNilTenantDB) {
		t.Fatalf("nil database error = %v, want ErrNilTenantDB", err)
	}
	if err := manager.Close(); err != nil { t.Fatal(err) }
	if err := manager.Close(); err != nil { t.Fatalf("second Close should be safe: %v", err) }
	if err := manager.Register("tenant", newTestDB(t)); !errors.Is(err, ErrManagerClosed) {
		t.Fatalf("Register after Close = %v, want ErrManagerClosed", err)
	}
	if _, err := manager.GetDB("tenant"); !errors.Is(err, ErrManagerClosed) {
		t.Fatalf("GetDB after Close = %v, want ErrManagerClosed", err)
	}
}

func TestMemoryManagerRegisterSameHandleDoesNotCloseIt(t *testing.T) {
	manager := NewMemoryManager()
	db := newTestDB(t)
	if err := manager.Register("tenant", db); err != nil { t.Fatal(err) }
	if err := manager.Register("tenant", db); err != nil { t.Fatal(err) }
	if err := db.Ping(); errors.Is(err, sql.ErrDBClosed) {
		t.Fatal("registering the same handle must not close it")
	}
	if err := manager.Close(); err != nil { t.Fatal(err) }
}
