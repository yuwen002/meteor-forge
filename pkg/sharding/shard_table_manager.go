package sharding

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// ShardTableManager 是一个外观（Facade）结构：把底层的 TableManager 和一个分片名 → 完整表名的
// 格式化函数组合起来，让调用方只需要处理分片名（如 "202611"、"7"），不用关心完整表名的拼装细节。
//
// 与 Scheduler 配合使用时，Scheduler 内部调 ShardTableManager 的方法来创建未来的分片表。
//
// 使用示例：
//
//	// 使用 SQL 模板创建分片表
//	mgr := sharding.NewMySQLTableManager(db)
//	stm := sharding.NewShardTableManager(mgr, "orders", `
//	    CREATE TABLE orders (
//	        id BIGINT PRIMARY KEY,
//	        user_id BIGINT NOT NULL,
//	        amount DECIMAL(10,2),
//	        created_at DATETIME,
//	        INDEX idx_user (user_id)
//	    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4
//	`, func(shardName string) string {
//	    return "orders_" + shardName
//	})
//	stm.CreateShardTable(ctx, "202611") // 直接用 SQL 模板建 orders_202611
//
//	// 或者用 CreateTableLike 从模板表克隆
//	stm2 := sharding.NewShardTableManager(mgr, "orders", "", nil)
//	stm2.CreateShardTableLike(ctx, "202612", "orders_202611")
type ShardTableManager struct {
	manager    TableManager
	baseName   string
	schemaTpl  string
	nameFormat func(string) string
}

// NewShardTableManager 创建 ShardTableManager。
//
// 参数：
//   - manager: 底层 TableManager 实现
//   - baseName: 基础表名前缀（如 "orders"），用于 ListShardTables 等枚举操作
//   - schemaTpl: CREATE TABLE DDL 模板（支持 LoadTemplateFromFile/LoadTemplateFromString 覆盖）
//   - fn: 分片名 → 完整表名的格式化函数；传 nil 时退化为直接用 shardName 作为表名
//
// 示例：
//
//	stm := sharding.NewShardTableManager(mgr, "orders", createTableSQL,
//	    func(shardName string) string { return "orders_" + shardName })
func NewShardTableManager(manager TableManager, baseName, schemaTpl string, fn func(string) string) *ShardTableManager {
	if fn == nil {
		fn = func(shardName string) string {
			return shardName
		}
	}
	return &ShardTableManager{
		manager:    manager,
		baseName:   baseName,
		schemaTpl:  schemaTpl,
		nameFormat: fn,
	}
}

// CreateShardTable 用已加载的 schemaTpl 创建一个分片表。
// nameFormat(shardName) 会被注入到 SQL 模板中。
//
// 示例：
//
//	stm.CreateShardTable(ctx, "202611") // 用模板建 orders_202611
func (m *ShardTableManager) CreateShardTable(ctx context.Context, shardName string) error {
	tableName := m.nameFormat(shardName)
	return m.manager.CreateTable(ctx, tableName, m.schemaTpl)
}

// CreateShardTableLike 克隆 sourceShard 的结构创建 newShard（含索引、约束）。
// 适用于模板表已经存在时快速创建新分片的场景。
//
// 示例：
//
//	stm.CreateShardTableLike(ctx, "202612", "202611")
func (m *ShardTableManager) CreateShardTableLike(ctx context.Context, newShardName, sourceShardName string) error {
	newTableName := m.nameFormat(newShardName)
	sourceTableName := m.nameFormat(sourceShardName)
	return m.manager.CreateTableLike(ctx, newTableName, sourceTableName)
}

// DropShardTable 删除一个分片表（IF EXISTS，幂等）。
//
// 示例：
//
//	stm.DropShardTable(ctx, "202401") // 清理过期分片
func (m *ShardTableManager) DropShardTable(ctx context.Context, shardName string) error {
	tableName := m.nameFormat(shardName)
	return m.manager.DropTable(ctx, tableName)
}

// TableExists 检查一个分片表是否存在。
//
// 示例：
//
//	ok, _ := stm.TableExists(ctx, "202611")
func (m *ShardTableManager) TableExists(ctx context.Context, shardName string) (bool, error) {
	tableName := m.nameFormat(shardName)
	return m.manager.TableExists(ctx, tableName)
}

// ListShardTables 列出数据库中所有以 baseName 开头的分片表。
// 可用于运维监控（当前有多少分片、最早/最晚的分片是什么）。
//
// 示例：
//
//	tables, _ := stm.ListShardTables(ctx)
//	fmt.Println("当前分片:", tables) // [orders_202401 ... orders_202611]
func (m *ShardTableManager) ListShardTables(ctx context.Context) ([]string, error) {
	return m.manager.ListTables(ctx, m.baseName)
}

// LoadTemplateFromFile 从磁盘读取 CREATE TABLE 模板。
// 启动时可以先编译好模板文件，再加载到 ShardTableManager。
//
// 示例：
//
//	stm.LoadTemplateFromFile("./templates/orders.sql")
func (m *ShardTableManager) LoadTemplateFromFile(filePath string) error {
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return fmt.Errorf("resolve absolute path: %v", err)
	}

	content, err := os.ReadFile(absPath)
	if err != nil {
		return fmt.Errorf("read template file: %v", err)
	}

	templateStr := string(content)
	if templateStr == "" {
		return fmt.Errorf("template file is empty")
	}

	m.schemaTpl = templateStr
	return nil
}

// LoadTemplateFromString 直接以字符串形式设置 CREATE TABLE 模板。
//
// 示例：
//
//	stm.LoadTemplateFromString(`CREATE TABLE orders (id BIGINT PRIMARY KEY) ...`)
func (m *ShardTableManager) LoadTemplateFromString(templateStr string) error {
	if templateStr == "" {
		return fmt.Errorf("template must not be empty")
	}
	m.schemaTpl = templateStr
	return nil
}

// GetCurrentTemplate 返回当前已加载的 DDL 模板字符串。
// 主要用于调试或日志记录。
//
// 示例：
//
//	log.Println("当前模板:", stm.GetCurrentTemplate())
func (m *ShardTableManager) GetCurrentTemplate() string {
	return m.schemaTpl
}