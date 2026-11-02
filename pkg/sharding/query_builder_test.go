package sharding

import (
	"strings"
	"testing"
)

// Find/First 的表名白名单校验：恶意表名必须在触达数据库之前被拒绝。
// db 传 nil 是安全的——校验失败即返回，不会走到 gorm 执行层。
func TestQueryBuilderRejectsUnsafeTableNames(t *testing.T) {
	rule := NewShardRule(ByMonth, "orders")
	cases := []string{
		"orders_2026; DROP TABLE users--",
		"orders_2026' OR '1'='1",
		"orders.`evil` x y",        // 三段以上
		"",                         // 空
		"orders-2026",              // 非法字符
		"orders_2026 UNION SELECT", // 注入尝试（且超两段）
	}
	for _, bad := range cases {
		qb := NewQueryBuilder(nil, rule).Tables(bad)

		var dest []map[string]interface{}
		err := qb.Find(&dest)
		if err == nil || !strings.Contains(err.Error(), "非法") {
			t.Errorf("Find should reject table name %q, got err=%v", bad, err)
		}

		var one map[string]interface{}
		err = qb.First(&one)
		if err == nil || !strings.Contains(err.Error(), "非法") {
			t.Errorf("First should reject table name %q, got err=%v", bad, err)
		}
	}
}

// 合法表名（含别名与反引号形式）应通过白名单校验。
// 同包内直接测试私有校验器，避免 nil db 触达执行层。
func TestQueryBuilderAcceptsLegalTableNames(t *testing.T) {
	rule := NewShardRule(ByMonth, "orders")
	qb := NewQueryBuilder(nil, rule).Tables("`orders_2026` o", "orders_2027", "users_1")
	if err := qb.validateTables(); err != nil {
		t.Errorf("legal table names wrongly rejected: %v", err)
	}
}

// 未指定表时的哨兵错误
func TestQueryBuilderRequiresTables(t *testing.T) {
	qb := NewQueryBuilder(nil, NewShardRule(ByIDHash, "users"))
	var dest []map[string]interface{}
	if err := qb.Find(&dest); err == nil || !strings.Contains(err.Error(), "未指定查询表") {
		t.Errorf("expected 未指定查询表, got %v", err)
	}
}
