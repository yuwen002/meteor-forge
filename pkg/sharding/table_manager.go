package sharding

import (
	"context"
	"strings"
)

// TableManager 是不同数据库的"表级别操作"抽象接口。
// 三种实现：MySQLTableManager / PostgresTableManager / ClickHouseTableManager。
// 所有方法对动态表名都会执行 isValidTableName 白名单校验（仅字母/数字/下划线），
// 防止表名注入。
//
// 典型用途：
//   - Scheduler 定时提前创建分片表
//   - 应用启动时预热检查
//   - 运维脚本（枚举所有分片表、清理废弃分片）
//
// 使用示例（MySQL）：
//
//	db, _ := gorm.Open(mysql.Open("..."), &gorm.Config{})
//	mgr := sharding.NewMySQLTableManager(db)
//	ctx := context.Background()
//
//	exists, _ := mgr.TableExists(ctx, "orders_202611")
//	if !exists {
//	    mgr.CreateTableLike(ctx, "orders_202611", "orders") // 克隆模板表结构
//	}
type TableManager interface {
	// TableExists 检查表是否存在
	TableExists(ctx context.Context, table string) (bool, error)
	// CreateTable 使用原始 DDL SQL 创建新表（会自动处理 SQL 中硬编码的旧表名 → 新表名）
	CreateTable(ctx context.Context, table, sql string) error
	// CreateTableLike 克隆 sourceTable 的完整结构（含索引、约束），创建为 newTable
	CreateTableLike(ctx context.Context, newTable, sourceTable string) error
	// DropTable 删除表（IF EXISTS，幂等）
	DropTable(ctx context.Context, table string) error
	// ListTables 列出所有以 prefix 开头的表（用于枚举一个逻辑实体的所有分片）
	ListTables(ctx context.Context, prefix string) ([]string, error)
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

// cleanSQLTemplate 清理SQL模板，移除单行注释和多余的空格
func cleanSQLTemplate(sql string) string {
	lines := strings.Split(sql, "\n")
	var cleanedLines []string

	for _, line := range lines {
		trimmedLine := strings.TrimSpace(line)
		if trimmedLine == "" || strings.HasPrefix(trimmedLine, "--") {
			continue
		}
		cleanedLines = append(cleanedLines, line)
	}

	cleanedSQL := strings.Join(cleanedLines, " ")
	cleanedSQL = strings.Join(strings.Fields(cleanedSQL), " ")

	return cleanedSQL
}