package sharding

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gorm.io/gorm"
)

// TableManager 定义了表管理器的接口
// 用于抽象不同数据库的表操作
type TableManager interface {
	// TableExists 检查表是否存在
	TableExists(ctx context.Context, table string) (bool, error)
	// CreateTable 创建新表
	CreateTable(ctx context.Context, table, sql string) error
	// CreateTableLike 创建一个与源表结构相同的新表
	CreateTableLike(ctx context.Context, newTable, sourceTable string) error
	// DropTable 删除表
	DropTable(ctx context.Context, table string) error
	// ListTables 列出所有匹配前缀的表
	ListTables(ctx context.Context, prefix string) ([]string, error)
}

// MySQLTableManager 是MySQL数据库的表管理器实现
type MySQLTableManager struct {
	db *gorm.DB // GORM数据库实例
}

// NewMySQLTableManager 创建MySQL表管理器实例
func NewMySQLTableManager(db *gorm.DB) *MySQLTableManager {
	return &MySQLTableManager{db: db}
}

// TableExists 检查表是否存在
func (m *MySQLTableManager) TableExists(ctx context.Context, table string) (bool, error) {
	var exists bool
	sql := `SELECT 1 FROM information_schema.tables 
            WHERE table_schema = DATABASE() AND table_name = ? LIMIT 1`
	err := m.db.WithContext(ctx).Raw(sql, table).Scan(&exists).Error
	return exists, err
}

// CreateTable 创建新表
// 注意：会自动添加 IF NOT EXISTS 子句
// 支持两种格式：
// 1. 完整的CREATE TABLE语句，会自动替换表名
// 2. 表定义（不包含CREATE TABLE部分）
func (m *MySQLTableManager) CreateTable(ctx context.Context, table, sql string) error {
	if !isValidTableName(table) {
		return fmt.Errorf("无效的表名: %s", table)
	}

	// 清理SQL，移除注释和多余的空格
	cleanSQL := cleanSQLTemplate(sql)

	var createSQL string
	if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(cleanSQL)), "CREATE TABLE") {
		// 如果是完整的CREATE TABLE语句，替换表名
		re := regexp.MustCompile(`(?i)CREATE\s+TABLE\s+[` + "`" + `]?\w+[` + "`" + `]?`)
		createSQL = re.ReplaceAllString(cleanSQL, fmt.Sprintf("CREATE TABLE IF NOT EXISTS `%s`", table))
	} else {
		// 否则认为是表定义，添加CREATE TABLE部分
		createSQL = fmt.Sprintf("CREATE TABLE IF NOT EXISTS `%s` %s", table, cleanSQL)
	}

	return m.db.WithContext(ctx).Exec(createSQL).Error
}

// cleanSQLTemplate 清理SQL模板，移除注释和多余的空格
func cleanSQLTemplate(sql string) string {
	// 移除单行注释
	lines := strings.Split(sql, "\n")
	var cleanedLines []string

	for _, line := range lines {
		// 跳过空行和注释行
		trimmedLine := strings.TrimSpace(line)
		if trimmedLine == "" || strings.HasPrefix(trimmedLine, "--") {
			continue
		}
		cleanedLines = append(cleanedLines, line)
	}

	// 重新组合为单行并规范化空格
	cleanedSQL := strings.Join(cleanedLines, " ")
	cleanedSQL = strings.Join(strings.Fields(cleanedSQL), " ") // 合并多个空格

	return cleanedSQL
}

// CreateTableLike 创建一个与源表结构相同的新表
// 参数:
//   - ctx: 上下文
//   - newTable: 新表名
//   - sourceTable: 源表名
//
// 返回:
//   - error: 创建表时的错误
//
// 示例:
//
//	err := manager.CreateTableLike(ctx, "new_orders", "orders")
//	if err != nil {
//	    log.Fatal(err)
//	}
func (m *MySQLTableManager) CreateTableLike(ctx context.Context, newTable, sourceTable string) error {
	if !isValidTableName(newTable) {
		return fmt.Errorf("invalid table name: %s", newTable)
	}
	if !isValidTableName(sourceTable) {
		return fmt.Errorf("invalid source table name: %s", sourceTable)
	}

	// 使用 MySQL 的 LIKE 语法创建结构相同的表
	sql := fmt.Sprintf("CREATE TABLE IF NOT EXISTS `%s` LIKE `%s`", newTable, sourceTable)
	return m.db.WithContext(ctx).Exec(sql).Error
}

// DropTable 删除表
// 注意：使用 IF EXISTS 避免表不存在时报错
func (m *MySQLTableManager) DropTable(ctx context.Context, table string) error {
	if !isValidTableName(table) {
		return fmt.Errorf("无效的表名: %s", table)
	}
	return m.db.WithContext(ctx).Exec(fmt.Sprintf("DROP TABLE IF EXISTS `%s`", table)).Error
}

// ListTables 列出所有匹配前缀的表
func (m *MySQLTableManager) ListTables(ctx context.Context, prefix string) ([]string, error) {
	var tables []string
	// 使用更精确的匹配模式：前缀后跟数字或下划线
	sql := `SELECT table_name FROM information_schema.tables 
           WHERE table_schema = DATABASE() 
           AND table_name REGEXP ?`

	// 构建正则表达式模式：^prefix_\d+$
	regexPattern := fmt.Sprintf("^%s(_\\d+)?$", regexp.QuoteMeta(prefix))
	err := m.db.WithContext(ctx).Raw(sql, regexPattern).Scan(&tables).Error
	return tables, err
}

// ShardTableManager 分片表管理器
// 提供高级别的分片表操作
type ShardTableManager struct {
	manager    TableManager        // 底层表管理器
	baseName   string              // 基础表名
	schemaTpl  string              // 建表SQL模板（不含表名）
	nameFormat func(string) string // 表名格式化函数
}

// NewShardTableManager 创建分片表管理器
// 参数：
//   - manager: 底层表管理器
//   - baseName: 基础表名
//   - schemaTpl: 建表SQL模板
//   - fn: 表名生成函数，如果为nil则使用默认格式：{baseName}_{shardName}
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

// CreateShardTable 创建分片表
func (m *ShardTableManager) CreateShardTable(ctx context.Context, shardName string) error {
	tableName := m.nameFormat(shardName)
	return m.manager.CreateTable(ctx, tableName, m.schemaTpl)
}

// CreateShardTableLike 创建一个与源分片表结构相同的新分片表
// 参数:
//   - ctx: 上下文
//   - newShardName: 新分片名
//   - sourceShardName: 源分片名
//
// 返回:
//   - error: 创建表时的错误
//
// 示例:
//
//	err := manager.CreateShardTableLike(ctx, "202308", "202307")
//	if err != nil {
//	    log.Fatal(err)
//	}
func (m *ShardTableManager) CreateShardTableLike(ctx context.Context, newShardName, sourceShardName string) error {
	newTableName := m.nameFormat(newShardName)
	sourceTableName := m.nameFormat(sourceShardName)
	return m.manager.CreateTableLike(ctx, newTableName, sourceTableName)
}

// DropShardTable 删除分片表
func (m *ShardTableManager) DropShardTable(ctx context.Context, shardName string) error {
	tableName := m.nameFormat(shardName)
	return m.manager.DropTable(ctx, tableName)
}

// TableExists 检查分片表是否存在
func (m *ShardTableManager) TableExists(ctx context.Context, shardName string) (bool, error) {
	tableName := m.nameFormat(shardName)
	return m.manager.TableExists(ctx, tableName)
}

// ListShardTables 列出所有分片表
func (m *ShardTableManager) ListShardTables(ctx context.Context) ([]string, error) {
	// 使用基础表名作为前缀，而不是基础表名加下划线
	return m.manager.ListTables(ctx, m.baseName)
}

// LoadTemplateFromFile 从文件加载表结构模板
// 参数：
//   - filePath: SQL模板文件路径，可以是绝对路径或相对于工作目录的路径
//
// 返回：
//   - error: 如果文件读取失败或内容为空则返回错误
func (m *ShardTableManager) LoadTemplateFromFile(filePath string) error {
	// 获取绝对路径
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return fmt.Errorf("获取绝对路径失败: %v", err)
	}

	// 读取文件内容
	content, err := os.ReadFile(absPath)
	if err != nil {
		return fmt.Errorf("读取模板文件失败: %v", err)
	}

	// 检查内容是否为空
	templateStr := string(content)
	if templateStr == "" {
		return fmt.Errorf("模板文件内容为空")
	}

	// 更新模板
	m.schemaTpl = templateStr
	return nil
}

// LoadTemplateFromString 从字符串加载表结构模板
// 参数：
//   - templateStr: 表结构SQL模板字符串
//
// 返回：
//   - error: 如果模板字符串为空则返回错误
func (m *ShardTableManager) LoadTemplateFromString(templateStr string) error {
	// 检查内容是否为空
	if templateStr == "" {
		return fmt.Errorf("模板内容不能为空")
	}

	// 更新模板
	m.schemaTpl = templateStr
	return nil
}

// GetCurrentTemplate 获取当前使用的表结构模板
func (m *ShardTableManager) GetCurrentTemplate() string {
	return m.schemaTpl
}

// isValidTableName 验证表名是否合法
// 表名只能包含字母、数字和下划线，且长度不能超过64个字符
func isValidTableName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z') &&
			!(r >= 'A' && r <= 'Z') &&
			!(r >= '0' && r <= '9') &&
			r != '_' {
			return false
		}
	}
	return true
}
