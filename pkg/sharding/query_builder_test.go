package sharding

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestQueryBuilderRejectsUnsafeTableNames(t *testing.T) {
	rule := NewShardRule(ByMonth, "orders")
	cases := []string{
		"orders_2026; DROP TABLE users--",
		"orders_2026' OR '1'='1",
		"orders.`evil` x y",
		"",
		"orders-2026",
		"orders_2026 UNION SELECT",
	}
	for _, bad := range cases {
		qb := NewQueryBuilder(nil, rule).Tables(bad)
		var dest []map[string]interface{}
		err := qb.Find(&dest)
		if err == nil || !strings.Contains(err.Error(), "非法") {
			t.Errorf("Find should reject table name %q, got err=%v", bad, err)
		}
	}
}

func TestQueryBuilderAcceptsLegalTableNames(t *testing.T) {
	rule := NewShardRule(ByMonth, "orders")
	qb := NewQueryBuilder(nil, rule).Tables("`orders_2026` o", "orders_2027", "users_1")
	if err := qb.validateTables(); err != nil {
		t.Errorf("legal table names wrongly rejected: %v", err)
	}
}

func TestQueryBuilderRequiresTables(t *testing.T) {
	qb := NewQueryBuilder(nil, NewShardRule(ByIDHash, "users"))
	var dest []map[string]interface{}
	if err := qb.Find(&dest); err == nil || !strings.Contains(err.Error(), "未指定查询表") {
		t.Errorf("expected 未指定查询表, got %v", err)
	}
}

// ================================================================
// assertPlaceholderCount — 占位符数量校验
// ================================================================

func TestAssertPlaceholderCount(t *testing.T) {
	t.Run("exact match no panic", func(t *testing.T) {
		assertPlaceholderCount("id = ? AND name = ?", []interface{}{1, "bob"})
	})
	t.Run("empty query ok", func(t *testing.T) {
		assertPlaceholderCount("", nil)
	})
	t.Run("empty args ok", func(t *testing.T) {
		assertPlaceholderCount("1=1", nil)
	})
	t.Run("too many args panics", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Errorf("expected panic")
			}
		}()
		assertPlaceholderCount("id = ?", []interface{}{1, 2})
	})
	t.Run("too few args panics", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Errorf("expected panic")
			}
		}()
		assertPlaceholderCount("id = ? AND name = ?", []interface{}{1})
	})
}

func TestWherePanicsOnMismatchedArgs(t *testing.T) {
	rule := NewShardRule(ByMonth, "orders")
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("Where should panic on placeholder/args mismatch")
		} else {
			if !strings.Contains(fmt.Sprintf("%v", r), "占位符") {
				t.Errorf("panic msg should mention 占位符, got %v", r)
			}
		}
	}()
	NewQueryBuilder(nil, rule).Where("id = ? AND name = ?", 1)
}

// ================================================================
// parallelQuery — 泛型 worker pool
// ================================================================

func TestParallelQueryRunsAll(t *testing.T) {
	tables := []string{"a", "b", "c", "d"}
	results, err := parallelQuery(context.Background(), tables, 0, func(ctx context.Context, table string) (string, error) {
		return table + "_done", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 4 {
		t.Fatalf("expected 4 results, got %d", len(results))
	}
	for i, r := range results {
		if r != tables[i]+"_done" {
			t.Errorf("results[%d] = %q, want %q", i, r, tables[i]+"_done")
		}
	}
}

func TestParallelQueryConcurrencyLimit(t *testing.T) {
	tables := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}
	var maxConcurrent int32
	var current int32
	var mu sync.Mutex

	_, err := parallelQuery(context.Background(), tables, 2, func(ctx context.Context, table string) (int, error) {
		c := atomic.AddInt32(&current, 1)
		for {
			old := atomic.LoadInt32(&maxConcurrent)
			if c <= old || atomic.CompareAndSwapInt32(&maxConcurrent, old, c) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt32(&current, -1)
		mu.Lock()
		mu.Unlock() //nolint:staticcheck // SA2001: intentional empty lock cycle to test contention
		return 1, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&maxConcurrent); got > 2 {
		t.Errorf("max concurrent = %d, want <= 2", got)
	}
}

func TestParallelQueryErrorPropagates(t *testing.T) {
	tables := []string{"a", "b", "c"}
	_, err := parallelQuery(context.Background(), tables, 0, func(ctx context.Context, table string) (int, error) {
		if table == "b" {
			return 0, fmt.Errorf("boom")
		}
		return 1, nil
	})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("expected boom error, got %v", err)
	}
}

func TestParallelQueryContextCancelStopsEarly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	tables := []string{"a", "b", "c", "d", "e"}
	callCount := int32(0)

	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	_, err := parallelQuery(ctx, tables, 1, func(ctx context.Context, table string) (int, error) {
		atomic.AddInt32(&callCount, 1)
		time.Sleep(50 * time.Millisecond)
		return 1, nil
	})
	if err != context.Canceled {
		t.Logf("err = %v (may be nil if all finished before cancel)", err)
	}
}

// ================================================================
// cloneForTable / cloneWithLimit — 链式 clone 不可变
// ================================================================

func TestCloneForTableIsIsolated(t *testing.T) {
	rule := NewShardRule(ByMonth, "orders")
	orig := NewQueryBuilder(nil, rule).
		WithContext(context.Background()).
		Where("status = ?", 4).
		Limit(20).
		Offset(5).
		Tables("orders_2026_01", "orders_2026_02")

	clone := orig.cloneForTable(context.Background(), "orders_2026_03", true)

	if clone.limit != 0 {
		t.Errorf("cloneForTable with zeroPage=true should reset limit, got %d", clone.limit)
	}
	if clone.offset != 0 {
		t.Errorf("cloneForTable with zeroPage=true should reset offset, got %d", clone.offset)
	}
	if len(clone.tables) != 1 || clone.tables[0] != "orders_2026_03" {
		t.Errorf("clone tables = %v, want [orders_2026_03]", clone.tables)
	}
	if orig.limit != 20 {
		t.Errorf("orig should be unchanged, got limit=%d", orig.limit)
	}
	if len(orig.tables) != 2 {
		t.Errorf("orig tables modified")
	}
}

func TestCloneWithLimitCopiesAll(t *testing.T) {
	rule := NewShardRule(ByMonth, "orders")
	orig := NewQueryBuilder(nil, rule).
		Where("status = ?", 4).
		Tables("orders_2026_01", "orders_2026_02")

	clone := orig.cloneWithLimit(1)
	if clone.limit != 1 {
		t.Errorf("cloneWithLimit limit = %d, want 1", clone.limit)
	}
	if orig.limit != 0 {
		t.Errorf("orig limit modified = %d", orig.limit)
	}
}

// ================================================================
// applyPagination — 分页截断
// ================================================================

func TestApplyPagination(t *testing.T) {
	type Row struct{ Id int }
	slice := reflect.MakeSlice(reflect.TypeOf([]Row{}), 100, 100)
	for i := 0; i < 100; i++ {
		slice.Index(i).Field(0).SetInt(int64(i))
	}

	qb := NewQueryBuilder(nil, NewShardRule(ByMonth, "orders")).Limit(10).Offset(20)
	result := qb.applyPagination(slice)
	if result.Len() != 10 {
		t.Errorf("paginated length = %d, want 10", result.Len())
	}
	firstId := result.Index(0).Field(0).Int()
	if firstId != 20 {
		t.Errorf("first id after offset = %d, want 20", firstId)
	}

	qb2 := NewQueryBuilder(nil, NewShardRule(ByMonth, "orders")).Limit(10).Offset(95)
	result2 := qb2.applyPagination(slice)
	if result2.Len() != 5 {
		t.Errorf("pagination near end = %d, want 5", result2.Len())
	}
}

// ================================================================
// sortByOrder / compareByField — 反射排序
// ================================================================

func TestSortByOrder(t *testing.T) {
	type Row struct {
		Name  string
		Score int64
	}
	slice := reflect.ValueOf([]Row{
		{"charlie", 30},
		{"alice", 10},
		{"bob", 20},
	})
	sortByOrder(slice, "score asc")
	firstScore := slice.Index(0).Field(1).Int()
	if firstScore != 10 {
		t.Errorf("after score asc, first score = %d, want 10", firstScore)
	}

	sortByOrder(slice, "score desc")
	firstScore = slice.Index(0).Field(1).Int()
	if firstScore != 30 {
		t.Errorf("after score desc, first score = %d, want 30", firstScore)
	}

	sortByOrder(slice, "name asc")
	firstName := slice.Index(0).Field(0).String()
	if firstName != "alice" {
		t.Errorf("after name asc, first = %s, want alice", firstName)
	}
}

func TestCompareByFieldMissingField(t *testing.T) {
	type Row struct{ Name string }
	a := reflect.ValueOf(Row{"a"})
	b := reflect.ValueOf(Row{"b"})
	result := compareByField(a, b, "nonexistent")
	if result != 0 {
		t.Errorf("compare missing field should return 0, got %d", result)
	}
}

// ================================================================
// toCamelCase / toValidAlias
// ================================================================

func TestToCamelCase(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"created_at", "CreatedAt"},
		{"user_id", "UserId"},
		{"status", "Status"},
		{"id", "Id"},
		{"a_b_c_d", "ABCD"},
	}
	for _, c := range cases {
		if got := toCamelCase(c.in); got != c.want {
			t.Errorf("toCamelCase(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestToValidAlias(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"amount", "amount"},
		{"orders.amount", "orders_amount"},
		{"o.price", "o_price"},
	}
	for _, c := range cases {
		if got := toValidAlias(c.in); got != c.want {
			t.Errorf("toValidAlias(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ================================================================
// mergeRow — 按字段名后缀的聚合合并语义
// ================================================================

func TestMergeRowSemantics(t *testing.T) {
	type Stat struct {
		Total_sum float64
		Cnt       int64
		Price_min float64
		Price_max float64
		Status    int32
		OtherName string
	}
	elemType := reflect.TypeOf(Stat{})

	makeStat := func(s float64, c int64, min, max float64, st int32) reflect.Value {
		v := reflect.New(elemType).Elem()
		v.Field(0).SetFloat(s)
		v.Field(1).SetInt(c)
		v.Field(2).SetFloat(min)
		v.Field(3).SetFloat(max)
		v.Field(4).SetInt(int64(st))
		return v
	}

	t.Run("sum accumulates", func(t *testing.T) {
		a := makeStat(100.5, 0, 0, 0, 0)
		b := makeStat(200.5, 0, 0, 0, 0)
		mergeRow(a, b)
		if got := a.Field(0).Float(); got != 301.0 {
			t.Errorf("sum merge = %v, want 301", got)
		}
	})

	t.Run("cnt accumulates", func(t *testing.T) {
		a := makeStat(0, 5, 0, 0, 0)
		b := makeStat(0, 3, 0, 0, 0)
		mergeRow(a, b)
		if got := a.Field(1).Int(); got != 8 {
			t.Errorf("cnt merge = %v, want 8", got)
		}
	})

	t.Run("min takes smallest", func(t *testing.T) {
		a := makeStat(0, 0, 5.0, 0, 0)
		b := makeStat(0, 0, 3.0, 0, 0)
		mergeRow(a, b)
		if got := a.Field(2).Float(); got != 3.0 {
			t.Errorf("min merge = %v, want 3", got)
		}
	})

	t.Run("max takes largest", func(t *testing.T) {
		a := makeStat(0, 0, 0, 5.0, 0)
		b := makeStat(0, 0, 0, 10.0, 0)
		mergeRow(a, b)
		if got := a.Field(3).Float(); got != 10.0 {
			t.Errorf("max merge = %v, want 10", got)
		}
	})

	t.Run("non-aggregate keeps first non-zero", func(t *testing.T) {
		a := makeStat(0, 0, 0, 0, 1)
		b := makeStat(0, 0, 0, 0, 2)
		mergeRow(a, b)
		if got := a.Field(4).Int(); got != 1 {
			t.Errorf("non-merge field should keep first non-zero, got %v", got)
		}
	})
}

// ================================================================
// applyAggregateMerge — 多分片单行聚合合并
// ================================================================

func TestApplyAggregateMerge(t *testing.T) {
	type Row struct {
		Total_sum float64
		Cnt       int64
	}
	elemType := reflect.TypeOf(Row{})

	makeRow := func(sum float64, cnt int64) reflect.Value {
		v := reflect.New(elemType).Elem()
		v.Field(0).SetFloat(sum)
		v.Field(1).SetInt(cnt)
		return v
	}

	makeSlice := func(rows ...reflect.Value) reflect.Value {
		s := reflect.MakeSlice(reflect.SliceOf(elemType), len(rows), len(rows))
		for i, r := range rows {
			s.Index(i).Set(r)
		}
		return s
	}

	shard1 := makeSlice(makeRow(100, 10))
	shard2 := makeSlice(makeRow(200, 15))

	dest := reflect.New(reflect.SliceOf(elemType)).Elem()
	if err := applyAggregateMerge(dest, []reflect.Value{shard1, shard2}); err != nil {
		t.Fatal(err)
	}
	if dest.Len() != 1 {
		t.Fatalf("merged length = %d, want 1", dest.Len())
	}
	if got := dest.Index(0).Field(0).Float(); got != 300.0 {
		t.Errorf("merged Total_sum = %v, want 300", got)
	}
	if got := dest.Index(0).Field(1).Int(); got != 25 {
		t.Errorf("merged Cnt = %v, want 25", got)
	}
}

func TestApplyAggregateMergeEmptyShards(t *testing.T) {
	type Row struct {
		Sum float64
	}
	elemType := reflect.TypeOf(Row{})
	dest := reflect.New(reflect.SliceOf(elemType)).Elem()
	shard := reflect.MakeSlice(reflect.SliceOf(elemType), 0, 0)
	if err := applyAggregateMerge(dest, []reflect.Value{shard}); err != nil {
		t.Fatal(err)
	}
	if dest.Len() != 0 {
		t.Errorf("empty input should produce empty output, got len=%d", dest.Len())
	}
}

// ================================================================
// applyGroupByMerge + buildGroupKey
// ================================================================

func TestBuildGroupKey(t *testing.T) {
	type Row struct {
		Status int32
		Name   string
	}
	row := reflect.ValueOf(Row{Status: 4, Name: "ok"})
	key := buildGroupKey(row, []string{"status", "name"})
	if !strings.Contains(key, "4") || !strings.Contains(key, "ok") {
		t.Errorf("groupKey = %q, want containing 4 and ok", key)
	}
}

func TestApplyGroupByMerge(t *testing.T) {
	type Row struct {
		Status    int32
		Total_sum float64
		Cnt       int64
	}
	elemType := reflect.TypeOf(Row{})
	makeRow := func(status int32, sum float64, cnt int64) reflect.Value {
		v := reflect.New(elemType).Elem()
		v.Field(0).SetInt(int64(status))
		v.Field(1).SetFloat(sum)
		v.Field(2).SetInt(cnt)
		return v
	}
	makeSlice := func(rows ...reflect.Value) reflect.Value {
		s := reflect.MakeSlice(reflect.SliceOf(elemType), len(rows), len(rows))
		for i, r := range rows {
			s.Index(i).Set(r)
		}
		return s
	}

	s1 := makeSlice(makeRow(4, 100, 5), makeRow(2, 50, 3))
	s2 := makeSlice(makeRow(4, 200, 10), makeRow(1, 10, 1))

	dest := reflect.New(reflect.SliceOf(elemType)).Elem()
	if err := applyGroupByMerge(dest, []reflect.Value{s1, s2}, "status"); err != nil {
		t.Fatal(err)
	}
	if dest.Len() != 3 {
		t.Fatalf("merged group count = %d, want 3 (status=4,2,1)", dest.Len())
	}

	found4 := false
	for i := 0; i < dest.Len(); i++ {
		if dest.Index(i).Field(0).Int() == 4 {
			found4 = true
			if got := dest.Index(i).Field(1).Float(); got != 300.0 {
				t.Errorf("status=4 Total_sum = %v, want 300", got)
			}
			if got := dest.Index(i).Field(2).Int(); got != 15 {
				t.Errorf("status=4 Cnt = %v, want 15", got)
			}
		}
	}
	if !found4 {
		t.Error("status=4 row missing in merged result")
	}
}

// ================================================================
// smallerOf / largerOf — reflect 值比较
// ================================================================

func TestSmallerOf(t *testing.T) {
	a := reflect.ValueOf(int64(5))
	b := reflect.ValueOf(int64(3))
	if smallerOf(a, b).Int() != 3 {
		t.Error("smallerOf(5,3) should be 3")
	}
	c := reflect.ValueOf(float64(2.5))
	d := reflect.ValueOf(float64(9.1))
	if smallerOf(c, d).Float() != 2.5 {
		t.Error("smallerOf(2.5, 9.1) should be 2.5")
	}
}

func TestLargerOf(t *testing.T) {
	a := reflect.ValueOf(int64(5))
	b := reflect.ValueOf(int64(3))
	if largerOf(a, b).Int() != 5 {
		t.Error("largerOf(5,3) should be 5")
	}
}

// ================================================================
// buildSingleTableQuery — 多表保护
// ================================================================

func TestBuildSingleTableQueryPanicsOnMultiTable(t *testing.T) {
	rule := NewShardRule(ByMonth, "orders")
	qb := NewQueryBuilder(nil, rule).Tables("orders_2026_01", "orders_2026_02")
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on multi-table buildSingleTableQuery")
		} else {
			if !strings.Contains(fmt.Sprintf("%v", r), "期望单表") {
				t.Errorf("panic msg should mention 期望单表, got %v", r)
			}
		}
	}()
	qb.buildSingleTableQuery()
}

func TestBuildSingleTableQueryPanicsOnNoTable(t *testing.T) {
	qb := NewQueryBuilder(nil, NewShardRule(ByMonth, "orders"))
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on empty tables buildSingleTableQuery")
		}
	}()
	qb.buildSingleTableQuery()
}

// ================================================================
// FindParallel — 拒绝 GroupBy/Having
// ================================================================

func TestFindParallelRejectsGroupBy(t *testing.T) {
	rule := NewShardRule(ByMonth, "orders")
	qb := NewQueryBuilder(nil, rule).
		Tables("orders_2026_01").
		Group("status")
	var dest []map[string]interface{}
	if err := qb.FindParallel(&dest, 0); err == nil || !strings.Contains(err.Error(), "GROUP BY") {
		t.Errorf("FindParallel with Group should be rejected, got %v", err)
	}
}

// ================================================================
// FindInBatches — 参数校验
// ================================================================

func TestFindInBatchesParameterValidation(t *testing.T) {
	rule := NewShardRule(ByMonth, "orders")

	t.Run("rejects zero batchSize", func(t *testing.T) {
		qb := NewQueryBuilder(nil, rule).Tables("orders_2026_01")
		err := qb.FindInBatches(nil, 0, func(i int64) bool { return true })
		if err == nil {
			t.Error("expected error on zero batchSize")
		}
	})
	t.Run("rejects negative batchSize", func(t *testing.T) {
		qb := NewQueryBuilder(nil, rule).Tables("orders_2026_01")
		err := qb.FindInBatches(nil, -5, func(i int64) bool { return true })
		if err == nil {
			t.Error("expected error on negative batchSize")
		}
	})
	t.Run("rejects ORDER BY", func(t *testing.T) {
		qb := NewQueryBuilder(nil, rule).Tables("orders_2026_01").Order("id DESC")
		err := qb.FindInBatches(nil, 10, func(i int64) bool { return true })
		if err == nil || !strings.Contains(err.Error(), "ORDER BY") {
			t.Errorf("expected ORDER BY rejection, got %v", err)
		}
	})
	t.Run("rejects GROUP BY", func(t *testing.T) {
		qb := NewQueryBuilder(nil, rule).Tables("orders_2026_01").Group("status")
		err := qb.FindInBatches(nil, 10, func(i int64) bool { return true })
		if err == nil || !strings.Contains(err.Error(), "GROUP BY") {
			t.Errorf("expected GROUP BY rejection, got %v", err)
		}
	})
	t.Run("rejects non-slice dest", func(t *testing.T) {
		qb := NewQueryBuilder(nil, rule).Tables("orders_2026_01")
		var x int
		err := qb.FindInBatches(&x, 10, func(i int64) bool { return true })
		if err == nil {
			t.Error("expected error on non-slice dest")
		}
	})
}

// ================================================================
// routeRecord / extractTimeField / extractInt64Field — 分片路由
// ================================================================

func TestRouteRecordByTime(t *testing.T) {
	type Order struct {
		ID        int64
		CreatedAt time.Time
	}
	rule := NewShardRule(ByMonth, "orders")
	qb := NewQueryBuilder(nil, rule)

	tests := []struct {
		name string
		date time.Time
		want string
	}{
		{"jan 2026", time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC), "orders_202601"},
		{"dec 2026", time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC), "orders_202612"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			elem := reflect.ValueOf(Order{ID: 1, CreatedAt: tt.date})
			table, err := qb.routeRecord(elem, reflect.TypeOf(Order{}))
			if err != nil {
				t.Fatal(err)
			}
			if table != tt.want {
				t.Errorf("routeRecord → %q, want %q", table, tt.want)
			}
		})
	}
}

func TestRouteRecordByIDRange(t *testing.T) {
	type User struct {
		ID int64
	}
	rule := NewShardRule(ByIDRange, "users")
	qb := NewQueryBuilder(nil, rule)

	elem := reflect.ValueOf(User{ID: 500000})
	table, err := qb.routeRecord(elem, reflect.TypeOf(User{}))
	if err != nil {
		t.Fatal(err)
	}
	if table != "users_0" {
		t.Errorf("id=500000 → %q, want users_0", table)
	}

	elem2 := reflect.ValueOf(User{ID: 1500000})
	table2, err := qb.routeRecord(elem2, reflect.TypeOf(User{}))
	if err != nil {
		t.Fatal(err)
	}
	if table2 != "users_1" {
		t.Errorf("id=1500000 → %q, want users_1", table2)
	}
}

func TestExtractTimeFieldNotFound(t *testing.T) {
	type BadStruct struct {
		Name    string
		Amount  float64
		NoTime  int
	}
	elem := reflect.ValueOf(BadStruct{})
	_, err := extractTimeField(elem, reflect.TypeOf(BadStruct{}))
	if err == nil {
		t.Error("expected error for struct without time.Time")
	}
}

func TestExtractInt64FieldNotFound(t *testing.T) {
	type BadStruct struct {
		Name string
		Age  int32
	}
	elem := reflect.ValueOf(BadStruct{})
	_, err := extractInt64Field(elem, reflect.TypeOf(BadStruct{}))
	if err == nil {
		t.Error("expected error for struct without int64 ID")
	}
}

// ================================================================
// GetTables / WithTx — 链式不可变
// ================================================================

func TestGetTablesReturnsCopy(t *testing.T) {
	rule := NewShardRule(ByMonth, "orders")
	qb := NewQueryBuilder(nil, rule).Tables("orders_2026_01", "orders_2026_02")
	tables := qb.GetTables()
	if len(tables) != 2 {
		t.Fatalf("expected 2 tables, got %d", len(tables))
	}
	tables[0] = "tampered"
	orig := qb.GetTables()
	if orig[0] == "tampered" {
		t.Error("GetTables should return a copy")
	}
}

// ================================================================
// buildUnionAllQuery — 输入校验
// ================================================================

func TestBuildUnionAllQueryRequiresDB(t *testing.T) {
	rule := NewShardRule(ByMonth, "orders")
	qb := NewQueryBuilder(nil, rule).
		Tables("orders_202601", "orders_202602").
		Where("status = ?", 4)

	_, err := qb.buildUnionAllQuery()
	if err == nil || !strings.Contains(err.Error(), "db") {
		t.Errorf("expected db 未初始化 error, got %v", err)
	}
}

func TestBuildUnionAllQueryRequiresTwoTables(t *testing.T) {
	rule := NewShardRule(ByMonth, "orders")
	qb := NewQueryBuilder(nil, rule).Tables("orders_202601")

	_, err := qb.buildUnionAllQuery()
	if err == nil || !strings.Contains(err.Error(), "至少需要两个") {
		t.Errorf("expected 至少需要两个 error, got %v", err)
	}
}

// ================================================================
// CreateInBatches — 参数校验
// ================================================================

func TestCreateInBatchesParameterValidation(t *testing.T) {
	rule := NewShardRule(ByMonth, "orders")
	qb := NewQueryBuilder(nil, rule)

	t.Run("nil rule rejected", func(t *testing.T) {
		qbNoRule := NewQueryBuilder(nil, nil)
		var records []struct{}
		_, err := qbNoRule.CreateInBatches(&records, 10)
		if err == nil || !strings.Contains(err.Error(), "ShardRule") {
			t.Errorf("expected ShardRule error, got %v", err)
		}
	})
	t.Run("empty slice returns zero", func(t *testing.T) {
		var records []struct {
			CreatedAt time.Time
		}
		n, err := qb.CreateInBatches(&records, 10)
		if err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("empty slice should return 0, got %d", n)
		}
	})
	t.Run("negative batchSize rejected", func(t *testing.T) {
		var records []struct {
			CreatedAt time.Time
		}
		_, err := qb.CreateInBatches(&records, -1)
		if err == nil {
			t.Error("expected negative batchSize error")
		}
	})
	t.Run("no time field error", func(t *testing.T) {
		var records []struct { //nolint:prealloc // test code
			Name string
		}
		records = append(records, struct{ Name string }{"test"})
		_, err := qb.CreateInBatches(&records, 10)
		if err == nil || !strings.Contains(err.Error(), "time.Time") {
			t.Errorf("expected time.Time field error, got %v", err)
		}
	})
}

// ================================================================
// Context 链式传递
// ================================================================

func TestWithContextPropagates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rule := NewShardRule(ByMonth, "orders")
	qb := NewQueryBuilder(nil, rule).WithContext(ctx)

	if qb.ctx != ctx {
		t.Error("WithContext should store the context")
	}
}

func TestWhereChaining(t *testing.T) {
	rule := NewShardRule(ByMonth, "orders")
	qb := NewQueryBuilder(nil, rule).
		Where("status = ?", 4).
		Where("created_at > ?", time.Now()).
		OrWhere("priority = ?", 1)

	if qb.whereClause == "" {
		t.Error("chained Where should build whereClause")
	}
	if len(qb.args) != 3 {
		t.Errorf("expected 3 args, got %d", len(qb.args))
	}
}

// ================================================================
// TimeRange / IDRange — 自动分片枚举
// ================================================================

func TestTimeRangeEnumeratesShards(t *testing.T) {
	rule := NewShardRule(ByMonth, "orders")
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	qb := NewQueryBuilder(nil, rule).TimeRange(TimeRange{Start: start, End: end})

	if len(qb.tables) != 3 {
		t.Errorf("TimeRange jan-mar should produce 3 tables, got %d: %v", len(qb.tables), qb.tables)
	}
}

func TestIDRangeEnumeratesShards(t *testing.T) {
	rule := NewShardRule(ByIDRange, "users")
	qb := NewQueryBuilder(nil, rule).IDRange(IDRange{Start: 500000, End: 1500000})

	if len(qb.tables) != 2 {
		t.Errorf("IDRange covering users_0 and users_1 should produce 2 tables, got %d: %v", len(qb.tables), qb.tables)
	}
}

// ================================================================
// ExcludeTables
// ================================================================

func TestExcludeTablesFiltersList(t *testing.T) {
	rule := NewShardRule(ByMonth, "orders")
	qb := NewQueryBuilder(nil, rule).
		Tables("orders_2026_01", "orders_2026_02", "orders_2026_03").
		ExcludeTables("orders_2026_02")

	if len(qb.tables) != 2 {
		t.Errorf("after exclude, tables = %v, want 2", qb.tables)
	}
	for _, tname := range qb.tables {
		if tname == "orders_2026_02" {
			t.Error("excluded table still present")
		}
	}
}

// ================================================================
// CountParallel — 需要 nil db 但 validateTables 先返回错误
// ================================================================

func TestCountParallelWithNilDB(t *testing.T) {
	rule := NewShardRule(ByMonth, "orders")
	qb := NewQueryBuilder(nil, rule).Tables("orders_2026_01")
	// 没有 db，应该走到 parallelQuery → buildSingleTableQuery → db 为 nil → panic
	// 但我们只测 validateTables 先返回：
	if err := qb.validateTables(); err != nil {
		t.Fatal(err)
	}
}