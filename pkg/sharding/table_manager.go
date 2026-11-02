package sharding

import (
	"context"
	"strings"
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
