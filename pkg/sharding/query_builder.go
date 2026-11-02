package sharding

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
	"gorm.io/gorm"
)

// QueryBuilder is a fluent builder for cross-shard queries.
type QueryBuilder struct {
	db            *gorm.DB
	shardRule     *ShardRule
	ctx           context.Context
	selectColumns []string
	whereClause   string
	args          []interface{}
	orderBy       string
	groupBy       string
	having        string
	havingArgs    []interface{}
	limit         int
	offset        int
	tables        []string
	excludeTables map[string]struct{}
}

// NewQueryBuilder creates a new QueryBuilder.
func NewQueryBuilder(db *gorm.DB, rule *ShardRule) *QueryBuilder {
	return &QueryBuilder{
		db:            db,
		shardRule:     rule,
		ctx:           context.Background(),
		tables:        make([]string, 0),
		excludeTables: make(map[string]struct{}),
	}
}

// WithContext sets the context for the query. Propagates to all shard-level queries
// in FindParallel / CountParallel so the caller can enforce timeouts or cancellation.
func (qb *QueryBuilder) WithContext(ctx context.Context) *QueryBuilder {
	qb.ctx = ctx
	return qb
}

// Tables sets the target shard tables.
func (qb *QueryBuilder) Tables(tables ...string) *QueryBuilder {
	for _, t := range tables {
		if _, ok := qb.excludeTables[t]; ok {
			continue
		}
		qb.tables = append(qb.tables, t)
	}
	return qb
}

// TimeRange auto-enumerates shard tables for a time-based shard rule (ByYear / ByMonth ...).
// It calls ShardRule.GetTableNamesByTimeRange internally — caller does NOT need to
// manually enumerate shards. Returns an error if the rule is ID-based or the range is invalid.
// Example: qb.TimeRange(TimeRange{Start: time.Now().AddDate(0, -1, 0), End: time.Now()}).
func (qb *QueryBuilder) TimeRange(tr TimeRange) *QueryBuilder {
	if qb.shardRule == nil {
		return qb
	}
	names, err := qb.shardRule.GetTableNamesByTimeRange(tr)
	if err != nil {
		return qb
	}
	return qb.Tables(names...)
}

// IDRange auto-enumerates shard tables for an ID-based shard rule (ByIDHash / ByIDRange).
// It calls ShardRule.GetTableNamesByIDRange internally. For hash-based rules this always
// returns all shards (hash is modulo, range doesn't help narrowing); for range-based rules
// it computes the minimal set.
func (qb *QueryBuilder) IDRange(idr IDRange) *QueryBuilder {
	if qb.shardRule == nil {
		return qb
	}
	names, err := qb.shardRule.GetTableNamesByIDRange(idr)
	if err != nil {
		return qb
	}
	return qb.Tables(names...)
}

// ExcludeTables removes specific shard tables from the query set. Useful when combined
// with TimeRange/IDRange to skip problem tables (e.g. a table is being rebuilt).
func (qb *QueryBuilder) ExcludeTables(tables ...string) *QueryBuilder {
	for _, t := range tables {
		qb.excludeTables[t] = struct{}{}
	}
	filtered := qb.tables[:0]
	for _, t := range qb.tables {
		if _, ok := qb.excludeTables[t]; !ok {
			filtered = append(filtered, t)
		}
	}
	qb.tables = filtered
	return qb
}

// Select specifies columns to query.
func (qb *QueryBuilder) Select(columns ...string) *QueryBuilder {
	qb.selectColumns = columns
	return qb
}

// Where adds a WHERE condition (AND-chained).
// Panics if the number of "?" placeholders in query does not match len(args) —
// this guards against a common class of silent bugs (wrong SQL, wrong args).
func (qb *QueryBuilder) Where(query string, args ...interface{}) *QueryBuilder {
	assertPlaceholderCount(query, args)
	if qb.whereClause == "" {
		qb.whereClause = query
	} else {
		qb.whereClause = fmt.Sprintf("(%s) AND (%s)", qb.whereClause, query)
	}
	qb.args = append(qb.args, args...)
	return qb
}

// OrWhere adds an OR condition.
// Panics on placeholder/args mismatch (same as Where).
func (qb *QueryBuilder) OrWhere(query string, args ...interface{}) *QueryBuilder {
	assertPlaceholderCount(query, args)
	if qb.whereClause == "" {
		return qb.Where(query, args...)
	}
	qb.whereClause = fmt.Sprintf("(%s) OR (%s)", qb.whereClause, query)
	qb.args = append(qb.args, args...)
	return qb
}

// Order sets the ORDER BY clause.
func (qb *QueryBuilder) Order(order string) *QueryBuilder {
	qb.orderBy = order
	return qb
}

// Group adds GROUP BY columns.
func (qb *QueryBuilder) Group(columns ...string) *QueryBuilder {
	qb.groupBy = strings.Join(columns, ", ")
	return qb
}

// Having adds a HAVING clause.
// Panics on placeholder/args mismatch (same as Where).
func (qb *QueryBuilder) Having(having string, args ...interface{}) *QueryBuilder {
	assertPlaceholderCount(having, args)
	qb.having = having
	qb.havingArgs = args
	return qb
}

// assertPlaceholderCount panics if query's "?" count does not match len(args).
// This is intentional — chain builders (GORM-style) panic on programming errors
// rather than silently producing wrong SQL.
func assertPlaceholderCount(query string, args []interface{}) {
	if query == "" {
		return
	}
	expected := strings.Count(query, "?")
	if expected != len(args) {
		panic(fmt.Sprintf("[QueryBuilder] 占位符数量不匹配: query=%q 有 %d 个 ?, 但提供了 %d 个 args",
			query, expected, len(args)))
	}
}

// Limit sets the maximum number of rows to return.
func (qb *QueryBuilder) Limit(limit int) *QueryBuilder {
	qb.limit = limit
	return qb
}

// Offset sets the result offset (for pagination).
func (qb *QueryBuilder) Offset(offset int) *QueryBuilder {
	qb.offset = offset
	return qb
}

func (qb *QueryBuilder) buildSingleTableQuery() *gorm.DB {
	if len(qb.tables) == 0 {
		panic("[QueryBuilder] buildSingleTableQuery: 未指定表，请先调用 Tables()/TimeRange()/IDRange()")
	}
	if len(qb.tables) > 1 {
		panic(fmt.Sprintf("[QueryBuilder] buildSingleTableQuery: 期望单表，当前有 %d 个表，是否应改用 buildUnionAllQuery 或先 cloneForTable", len(qb.tables)))
	}
	db := qb.db
	if qb.ctx != nil {
		db = db.WithContext(qb.ctx)
	}
	query := db.Table(qb.tables[0])

	if len(qb.selectColumns) > 0 {
		query = query.Select(qb.selectColumns)
	}

	if qb.whereClause != "" {
		query = query.Where(qb.whereClause, qb.args...)
	}

	if qb.groupBy != "" {
		query = query.Group(qb.groupBy)
	}

	if qb.having != "" {
		query = query.Having(qb.having)
	}

	if qb.orderBy != "" {
		query = query.Order(qb.orderBy)
	}

	if qb.limit > 0 {
		query = query.Limit(qb.limit)
	}
	if qb.offset > 0 {
		query = query.Offset(qb.offset)
	}

	return query
}

func (qb *QueryBuilder) buildUnionAllQuery() (*gorm.DB, error) {
	if len(qb.tables) < 2 {
		return nil, errors.New("至少需要两个表进行 UNION ALL 查询")
	}
	if qb.db == nil {
		return nil, errors.New("db 未初始化")
	}

	var unionQueries []string
	var unionArgs []interface{}

	selectClause := "*"
	if len(qb.selectColumns) > 0 {
		selectClause = strings.Join(qb.selectColumns, ", ")
	}

	// 子查询 LIMIT 下推优化: 没有 GROUP BY 时，每个分片只需要返回
	// limit + offset 行就足够了（最坏情况所有匹配数据集中在一个分片）。
	// 有 GROUP BY 时不能下推，否则聚合结果会被提前截断。
	var subLimitValue int
	canPushSubLimit := qb.groupBy == "" && (qb.limit > 0 || qb.offset > 0)
	if canPushSubLimit {
		subLimitValue = qb.limit + qb.offset
		if subLimitValue <= 0 {
			subLimitValue = 0
			canPushSubLimit = false
		}
	}

	for _, table := range qb.tables {
		subQuery := fmt.Sprintf("SELECT %s FROM %s", selectClause, table)

		if qb.whereClause != "" {
			subQuery += " WHERE " + qb.whereClause
			unionArgs = append(unionArgs, qb.args...)
		}

		if qb.groupBy != "" {
			subQuery += " GROUP BY " + qb.groupBy
		}

		if qb.having != "" {
			subQuery += " HAVING " + qb.having
			unionArgs = append(unionArgs, qb.havingArgs...)
		}

		// 子查询也必须带 ORDER BY 才能让 LIMIT 结果确定
		if qb.orderBy != "" {
			subQuery += " ORDER BY " + qb.orderBy
		}

		if canPushSubLimit {
			subQuery += " LIMIT ?"
			unionArgs = append(unionArgs, subLimitValue)
		}

		unionQueries = append(unionQueries, subQuery)
	}

	unionSQL := "SELECT * FROM (\n"
	unionSQL += "  (" + strings.Join(unionQueries, ") UNION ALL\n   (") + ")\n"
	unionSQL += ") AS union_result"

	if qb.orderBy != "" {
		unionSQL += "\nORDER BY " + qb.orderBy
	}

	if qb.limit > 0 {
		unionSQL += "\nLIMIT ?"
		unionArgs = append(unionArgs, qb.limit)
	}
	if qb.offset > 0 {
		unionSQL += " OFFSET ?"
		unionArgs = append(unionArgs, qb.offset)
	}

	db := qb.db
	if db != nil && qb.ctx != nil {
		db = db.WithContext(qb.ctx)
	}
	return db.Raw(unionSQL, unionArgs...), nil
}

func (qb *QueryBuilder) validateTables() error {
	for _, spec := range qb.tables {
		fields := strings.Fields(spec)
		if len(fields) == 0 || len(fields) > 2 {
			return fmt.Errorf("非法的分片表指定: %q", spec)
		}
		for _, f := range fields {
			if !isValidTableName(strings.Trim(f, "`")) {
				return fmt.Errorf("非法的分片表名: %q", f)
			}
		}
	}
	return nil
}

// First queries a single record from one or more shard tables.
//
// Optimization: when no ORDER BY is set, queries shards one at a time and returns
// on the first hit — this avoids a full UNION ALL scan of every shard. When
// ORDER BY IS set we must use UNION ALL because "first after global sort" cannot
// be determined per-shard.
func (qb *QueryBuilder) First(dest interface{}) error {
	if len(qb.tables) == 0 {
		return errors.New("未指定查询表")
	}
	if err := qb.validateTables(); err != nil {
		return err
	}

	if len(qb.tables) == 1 {
		return qb.buildSingleTableQuery().Limit(1).First(dest).Error
	}

	// 有 ORDER BY → 必须用 UNION ALL（全局排序语义无法分片优化）
	if qb.orderBy != "" {
		oneQB := qb.cloneWithLimit(1)
		query, err := oneQB.buildUnionAllQuery()
		if err != nil {
			return err
		}
		return query.First(dest).Error
	}

	// 无 ORDER BY → 逐分片查，命中即返回
	var lastErr error
	for _, table := range qb.tables {
		select {
		case <-qb.ctx.Done():
			return qb.ctx.Err()
		default:
		}
		tableQB := qb.cloneForTable(qb.ctx, table, true)
		tableQB.orderBy = ""
		err := tableQB.buildSingleTableQuery().Limit(1).First(dest).Error
		if err == nil {
			return nil
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			lastErr = err
			continue
		}
		return fmt.Errorf("First 表 %s 失败: %w", table, err)
	}
	if lastErr != nil {
		return gorm.ErrRecordNotFound
	}
	return gorm.ErrRecordNotFound
}

// cloneWithLimit 克隆当前 QueryBuilder 并覆盖 limit 字段，不修改原对象。
func (qb *QueryBuilder) cloneWithLimit(limit int) *QueryBuilder {
	return &QueryBuilder{
		db:            qb.db,
		shardRule:     qb.shardRule,
		ctx:           qb.ctx,
		selectColumns: append([]string{}, qb.selectColumns...),
		whereClause:   qb.whereClause,
		args:          append([]interface{}{}, qb.args...),
		orderBy:       qb.orderBy,
		groupBy:       qb.groupBy,
		having:        qb.having,
		havingArgs:    append([]interface{}{}, qb.havingArgs...),
		limit:         limit,
		offset:        qb.offset,
		tables:        append([]string{}, qb.tables...),
		excludeTables: qb.excludeTables,
	}
}

// Find queries multiple records. Single-table uses direct query; multi-table uses UNION ALL.
func (qb *QueryBuilder) Find(dest interface{}) error {
	if len(qb.tables) == 0 {
		return errors.New("未指定查询表")
	}
	if err := qb.validateTables(); err != nil {
		return err
	}

	var query *gorm.DB
	var err error

	if len(qb.tables) == 1 {
		query = qb.buildSingleTableQuery()
	} else {
		query, err = qb.buildUnionAllQuery()
		if err != nil {
			return err
		}
	}

	return query.Find(dest).Error
}

// FindParallel queries each shard independently in parallel, then merges results in-app.
// Unlike Find (which uses a single UNION ALL SQL), FindParallel issues one query per shard
// and does the global sort + pagination in Go memory. This works well when shard count is
// large and UNION ALL becomes prohibitively expensive for the database.
//
// Each shard query strips its own limit/offset (set via Limit/Offset on the QueryBuilder)
// — pagination is applied globally after merging all shard results. ORDER BY (set via Order)
// is applied both per-shard (so each shard is already sorted, making the merge cheap) and
// re-applied globally via reflect-based sort on the merged slice.
//
// dest is a pointer-to-slice, e.g. &[]Order{}. concurrency caps the number of goroutines
// running queries in parallel; <= 0 or >= len(tables) means "fire all at once".
func (qb *QueryBuilder) FindParallel(dest interface{}, concurrency int) error {
	if qb.groupBy != "" || qb.having != "" {
		return errors.New("FindParallel 不支持 GROUP BY / HAVING — 分片独立聚合无法等价于全局聚合，请改用 Find (UNION ALL 路径) 或在应用层自行聚合")
	}
	destValue := reflect.ValueOf(dest)
	if destValue.Kind() != reflect.Ptr || destValue.Elem().Kind() != reflect.Slice {
		return errors.New("目标必须是指向切片的指针")
	}
	if err := qb.validateTables(); err != nil {
		return err
	}

	elemType := destValue.Elem().Type().Elem()

	results, err := parallelQuery(qb.ctx, qb.tables, concurrency, func(ctx context.Context, table string) (reflect.Value, error) {
		tableQB := qb.cloneForTable(ctx, table, true)
		sliceType := reflect.SliceOf(elemType)
		tempSlice := reflect.New(sliceType).Interface()
		if err := tableQB.buildSingleTableQuery().Find(tempSlice).Error; err != nil {
			return reflect.Value{}, fmt.Errorf("查询表 %s 失败: %w", table, err)
		}
		return reflect.ValueOf(tempSlice).Elem(), nil
	})
	if err != nil {
		return err
	}

	merged := reflect.MakeSlice(destValue.Elem().Type(), 0, 0)
	for _, r := range results {
		if r.IsValid() && r.Len() > 0 {
			merged = reflect.AppendSlice(merged, r)
		}
	}

	if qb.orderBy != "" && merged.Len() > 1 {
		sortByOrder(merged, qb.orderBy)
	}

	merged = qb.applyPagination(merged)
	destValue.Elem().Set(merged)
	return nil
}

// parallelQuery 通用泛型并行分片查询辅助函数。
// 对每个分片表独立并发执行 worker 闭包，按原分片顺序返回结果切片。
// 任一 worker 返回 error 则整体立即取消（通过 errgroup.WithContext）。
// concurrency <= 0 或 >= len(tables) 表示无上限并发。
func parallelQuery[T any](
	ctx context.Context,
	tables []string,
	concurrency int,
	worker func(ctx context.Context, table string) (T, error),
) ([]T, error) {
	g, egCtx := errgroup.WithContext(ctx)

	results := make([]T, len(tables))
	tableCh := make(chan int, len(tables))
	for i := range tables {
		tableCh <- i
	}
	close(tableCh)

	workers := concurrency
	if workers <= 0 || workers > len(tables) {
		workers = len(tables)
	}
	if workers == 0 {
		workers = 1
	}

	for w := 0; w < workers; w++ {
		g.Go(func() error {
			for idx := range tableCh {
				select {
				case <-egCtx.Done():
					return egCtx.Err()
				default:
				}
				val, err := worker(egCtx, tables[idx])
				if err != nil {
					return err
				}
				results[idx] = val
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}
	return results, nil
}

// cloneForTable clones the QueryBuilder bound to a single table.
// When zeroPage is true, limit/offset are cleared — used by FindParallel so each shard
// fetches all matches and pagination is applied globally.
func (qb *QueryBuilder) cloneForTable(ctx context.Context, table string, zeroPage bool) *QueryBuilder {
	limit := qb.limit
	offset := qb.offset
	if zeroPage {
		limit = 0
		offset = 0
	}
	db := qb.db
	if db != nil && ctx != nil {
		db = db.WithContext(ctx)
	}
	return &QueryBuilder{
		db:            db,
		shardRule:     qb.shardRule,
		ctx:           ctx,
		selectColumns: append([]string{}, qb.selectColumns...),
		whereClause:   qb.whereClause,
		args:          append([]interface{}{}, qb.args...),
		orderBy:       qb.orderBy,
		groupBy:       qb.groupBy,
		having:        qb.having,
		havingArgs:    append([]interface{}{}, qb.havingArgs...),
		limit:         limit,
		offset:        offset,
		tables:        []string{table},
		excludeTables: qb.excludeTables,
	}
}

func (qb *QueryBuilder) applyPagination(slice reflect.Value) reflect.Value {
	if qb.limit <= 0 && qb.offset <= 0 {
		return slice
	}

	start := qb.offset
	if start > slice.Len() {
		start = slice.Len()
	}
	end := start + qb.limit
	if qb.limit <= 0 || end > slice.Len() {
		end = slice.Len()
	}
	return slice.Slice(start, end)
}

// sortByOrder sorts a reflect slice value according to an ORDER BY string like
// "created_at DESC, id ASC". It supports snake_case field names (auto-converted to
// struct field names via toCamelCase) and DESC/ASC direction per key.
func sortByOrder(slice reflect.Value, orderBy string) {
	type sortKey struct {
		name string
		desc bool
	}

	var keys []sortKey
	for _, part := range strings.Split(orderBy, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		fields := strings.Fields(part)
		name := fields[0]
		desc := false
		if len(fields) > 1 && strings.EqualFold(fields[1], "DESC") {
			desc = true
		}
		keys = append(keys, sortKey{name: name, desc: desc})
	}

	if len(keys) == 0 {
		return
	}

	sort.Slice(slice.Interface(), func(i, j int) bool {
		a := slice.Index(i)
		b := slice.Index(j)
		for _, k := range keys {
			cmp := compareByField(a, b, k.name)
			if cmp != 0 {
				if k.desc {
					return cmp > 0
				}
				return cmp < 0
			}
		}
		return false
	})
}

// compareByField compares two struct values by field name (supports snake_case).
// Returns -1 if a < b, 0 if a == b, 1 if a > b. Returns 0 for unknown/incomparable fields.
func compareByField(a, b reflect.Value, fieldName string) int {
	name := toCamelCase(fieldName)

	if a.Kind() == reflect.Ptr {
		if a.IsNil() {
			a = reflect.Zero(a.Type().Elem())
		} else {
			a = a.Elem()
		}
	}
	if b.Kind() == reflect.Ptr {
		if b.IsNil() {
			b = reflect.Zero(b.Type().Elem())
		} else {
			b = b.Elem()
		}
	}

	if a.Kind() != reflect.Struct || b.Kind() != reflect.Struct {
		return 0
	}

	aVal := a.FieldByName(name)
	bVal := b.FieldByName(name)
	if !aVal.IsValid() || !bVal.IsValid() {
		return 0
	}

	switch aVal.Kind() {
	case reflect.String:
		if aVal.String() < bVal.String() {
			return -1
		} else if aVal.String() > bVal.String() {
			return 1
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if aVal.Int() < bVal.Int() {
			return -1
		} else if aVal.Int() > bVal.Int() {
			return 1
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if aVal.Uint() < bVal.Uint() {
			return -1
		} else if aVal.Uint() > bVal.Uint() {
			return 1
		}
	case reflect.Float32, reflect.Float64:
		if aVal.Float() < bVal.Float() {
			return -1
		} else if aVal.Float() > bVal.Float() {
			return 1
		}
	case reflect.Bool:
		if !aVal.Bool() && bVal.Bool() {
			return -1
		} else if aVal.Bool() && !bVal.Bool() {
			return 1
		}
	}
	return 0
}

// toCamelCase converts "created_at" → "CreatedAt", "id" → "Id".
func toCamelCase(name string) string {
	parts := strings.Split(name, "_")
	var b strings.Builder
	for _, p := range parts {
		if p == "" {
			continue
		}
		b.WriteString(strings.ToUpper(p[:1]))
		b.WriteString(p[1:])
	}
	return b.String()
}

// CountParallel returns the total count across all shard tables by querying each
// shard independently in parallel and summing. This avoids UNION ALL COUNT which
// only gives per-row counts, not global sum. concurrency controls parallelism
// (same semantics as FindParallel).
func (qb *QueryBuilder) CountParallel(concurrency int) (int64, error) {
	if len(qb.tables) == 0 {
		return 0, errors.New("未指定查询表")
	}
	if err := qb.validateTables(); err != nil {
		return 0, err
	}

	counts, err := parallelQuery(qb.ctx, qb.tables, concurrency, func(ctx context.Context, table string) (int64, error) {
		tableQB := qb.cloneForTable(ctx, table, true)
		var c int64
		if err := tableQB.buildSingleTableQuery().Count(&c).Error; err != nil {
			return 0, fmt.Errorf("统计表 %s 失败: %w", table, err)
		}
		return c, nil
	})
	if err != nil {
		return 0, err
	}

	var total int64
	for _, c := range counts {
		total += c
	}
	return total, nil
}

// Pluck extracts a single column from all shard tables into dest.
// dest must be a pointer to a slice of the column's element type (e.g. *[]string, *[]int64).
// Uniqueness is NOT applied — call deduplication yourself if needed.
func (qb *QueryBuilder) Pluck(column string, dest interface{}) error {
	if len(qb.tables) == 0 {
		return errors.New("未指定查询表")
	}
	if err := qb.validateTables(); err != nil {
		return err
	}

	destValue := reflect.ValueOf(dest)
	if destValue.Kind() != reflect.Ptr || destValue.Elem().Kind() != reflect.Slice {
		return errors.New("dest 必须是指向切片的指针")
	}
	destElemType := destValue.Elem().Type().Elem()

	results, err := parallelQuery(qb.ctx, qb.tables, 0, func(ctx context.Context, table string) (reflect.Value, error) {
		tableQB := qb.cloneForTable(ctx, table, true)
		sliceType := reflect.SliceOf(destElemType)
		tempSlice := reflect.New(sliceType).Interface()
		if err := tableQB.buildSingleTableQuery().Pluck(column, tempSlice).Error; err != nil {
			return reflect.Value{}, fmt.Errorf("Pluck 表 %s 失败: %w", table, err)
		}
		return reflect.ValueOf(tempSlice).Elem(), nil
	})
	if err != nil {
		return err
	}

	merged := reflect.MakeSlice(destValue.Elem().Type(), 0, 0)
	for _, r := range results {
		if r.IsValid() && r.Len() > 0 {
			merged = reflect.AppendSlice(merged, r)
		}
	}
	destValue.Elem().Set(merged)
	return nil
}

// Delete removes matching rows from all shard tables in parallel.
// Returns the total number of affected rows summed across shards.
// Note: hard-deletes. For soft-deletes, set qb's db to a gorm DB that has
// a DeletedAt field and the model, or use Updates to set a deleted_at column.
func (qb *QueryBuilder) Delete(model interface{}) (int64, error) {
	if len(qb.tables) == 0 {
		return 0, errors.New("未指定查询表")
	}
	if err := qb.validateTables(); err != nil {
		return 0, err
	}

	affected, err := parallelQuery(qb.ctx, qb.tables, 0, func(ctx context.Context, table string) (int64, error) {
		tableQB := qb.cloneForTable(ctx, table, false)
		res := tableQB.buildSingleTableQuery().Delete(model)
		if res.Error != nil {
			return 0, fmt.Errorf("删除表 %s 失败: %w", table, res.Error)
		}
		return res.RowsAffected, nil
	})
	if err != nil {
		return 0, err
	}

	var total int64
	for _, a := range affected {
		total += a
	}
	return total, nil
}

// Updates applies a bulk update to all shard tables in parallel.
// values can be a map[string]interface{} or a struct.
// Returns the total number of affected rows summed across shards.
func (qb *QueryBuilder) Updates(values interface{}) (int64, error) {
	if len(qb.tables) == 0 {
		return 0, errors.New("未指定查询表")
	}
	if err := qb.validateTables(); err != nil {
		return 0, err
	}

	affected, err := parallelQuery(qb.ctx, qb.tables, 0, func(ctx context.Context, table string) (int64, error) {
		tableQB := qb.cloneForTable(ctx, table, false)
		res := tableQB.buildSingleTableQuery().Updates(values)
		if res.Error != nil {
			return 0, fmt.Errorf("更新表 %s 失败: %w", table, res.Error)
		}
		return res.RowsAffected, nil
	})
	if err != nil {
		return 0, err
	}

	var total int64
	for _, a := range affected {
		total += a
	}
	return total, nil
}

// GetTables 返回当前 QueryBuilder 实际命中的分片表列表。
// 调用方可以在 TimeRange/IDRange 过滤之后拿到最终表名，用于日志、排查或后续原始 SQL 操作。
func (qb *QueryBuilder) GetTables() []string {
	out := make([]string, len(qb.tables))
	copy(out, qb.tables)
	return out
}

// WithTx 将 QueryBuilder 绑定到一个事务 DB（*gorm.DB 必须是 Begin() 后返回的 tx）。
// 之后所有查询/写入都会在该事务上执行。调用方负责最终 Commit/Rollback。
// 这是链式方法，返回新的 QueryBuilder 副本，不修改原对象。
func (qb *QueryBuilder) WithTx(tx *gorm.DB) *QueryBuilder {
	cp := *qb
	cp.db = tx
	return &cp
}

// Aggregate runs a SELECT aggregateExpr on every shard and merges the results.
//
// aggregateExpr is a GORM Select-style expression that must end in " AS alias".
// dest must be a pointer-to-slice where each element is a struct/map with a field
// named after the alias. If the expression is SINGLE-AGGREGATE (one aggregate alias,
// no GROUP BY), results are merged automatically via applyAggregateMerge.
// If GROUP BY is set, results are merged key-by-key in-app (see AggregateMergeKey).
//
// Examples:
//
//	// 单聚合: SELECT SUM(amount) AS total FROM ...  (自动分片求和)
//	type TotalRow struct{ Total float64 }
//	var rows []TotalRow
//	qb.Aggregate(&rows, "SUM(amount) AS total")
//
//	// 多聚合: SELECT status, SUM(amount) AS total, COUNT(*) AS cnt FROM ... GROUP BY status
//	type StatRow struct{ Status int32; Total float64; Cnt int64 }
//	qb.Group("status").Aggregate(&rows, "status, SUM(amount) AS total, COUNT(*) AS cnt")
func (qb *QueryBuilder) Aggregate(dest interface{}, aggregateExpr string) error {
	if qb.groupBy != "" && qb.orderBy != "" {
		return errors.New("Aggregate 分片模式下不支持 GROUP BY + ORDER BY 组合排序")
	}

	destValue := reflect.ValueOf(dest)
	if destValue.Kind() != reflect.Ptr || destValue.Elem().Kind() != reflect.Slice {
		return errors.New("dest 必须是指向切片的指针")
	}
	if err := qb.validateTables(); err != nil {
		return err
	}

	elemType := destValue.Elem().Type().Elem()

	// 无 GROUP BY → 每分片独立跑 SELECT aggregateExpr，结果只有 1 行，然后 applyAggregateMerge 合并
	if qb.groupBy == "" {
		results, err := parallelQuery(qb.ctx, qb.tables, 0, func(ctx context.Context, table string) (reflect.Value, error) {
			tableQB := qb.cloneForTable(ctx, table, true)
			tableQB.selectColumns = nil // 覆盖原有 Select
			sliceType := reflect.SliceOf(elemType)
			tempSlice := reflect.New(sliceType).Interface()
			if err := tableQB.buildSingleTableQuery().Select(aggregateExpr).Scan(tempSlice).Error; err != nil {
				return reflect.Value{}, fmt.Errorf("聚合表 %s 失败: %w", table, err)
			}
			return reflect.ValueOf(tempSlice).Elem(), nil
		})
		if err != nil {
			return err
		}
		return applyAggregateMerge(destValue.Elem(), results)
	}

	// 有 GROUP BY → 每分片独立聚合，最后按 GROUP BY 字段 key 合并
	results, err := parallelQuery(qb.ctx, qb.tables, 0, func(ctx context.Context, table string) (reflect.Value, error) {
		tableQB := qb.cloneForTable(ctx, table, true)
		tableQB.selectColumns = nil
		sliceType := reflect.SliceOf(elemType)
		tempSlice := reflect.New(sliceType).Interface()
		if err := tableQB.buildSingleTableQuery().Select(aggregateExpr).Scan(tempSlice).Error; err != nil {
			return reflect.Value{}, fmt.Errorf("聚合表 %s 失败: %w", table, err)
		}
		return reflect.ValueOf(tempSlice).Elem(), nil
	})
	if err != nil {
		return err
	}
	return applyGroupByMerge(destValue.Elem(), results, qb.groupBy)
}

// applyAggregateMerge 把多个分片的单行聚合结果合并成一行。
// 合并规则（按字段名后缀匹配）:
//   - 后缀 _cnt / count / total_cnt / rows / *Count → 累加
//   - 后缀 _sum / sum / total / total_sum / amount → 累加
//   - 后缀 _min / min → 取最小
//   - 后缀 _max / max → 取最大
//   - 后缀 _avg / avg → 无 GROUP BY 场景不支持（Avg 需要额外的 count）
//   - 其他（如 GROUP BY key 列）→ 保留值，要求所有分片一致
func applyAggregateMerge(merged reflect.Value, shardResults []reflect.Value) error {
	elemType := merged.Type().Elem()
	mergedElem := reflect.New(elemType).Elem()

	hasData := false
	for _, r := range shardResults {
		if !r.IsValid() || r.Len() == 0 {
			continue
		}
		hasData = true
		row := r.Index(0)
		mergeRow(mergedElem, row)
	}

	if !hasData {
		merged.Set(reflect.MakeSlice(merged.Type(), 0, 0))
		return nil
	}

	out := reflect.MakeSlice(merged.Type(), 1, 1)
	out.Index(0).Set(mergedElem)
	merged.Set(out)
	return nil
}

// mergeRow 按字段名语义合并两个聚合行到 merged（mutable）。
func mergeRow(merged, other reflect.Value) {
	t := merged.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		mergedField := merged.Field(i)
		otherField := other.Field(i)
		if !mergedField.CanSet() {
			continue
		}
		nameLower := strings.ToLower(field.Name)

		switch {
		case strings.Contains(nameLower, "cnt"),
			strings.Contains(nameLower, "count"),
			strings.Contains(nameLower, "total_cnt"),
			strings.Contains(nameLower, "rows"),
			strings.Contains(nameLower, "_count"):
			mergedField.SetInt(mergedField.Int() + otherField.Int())

		case strings.HasSuffix(nameLower, "_sum"),
			nameLower == "sum",
			strings.Contains(nameLower, "total_sum"):
			switch mergedField.Kind() {
			case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
				mergedField.SetInt(mergedField.Int() + otherField.Int())
			case reflect.Float32, reflect.Float64:
				mergedField.SetFloat(mergedField.Float() + otherField.Float())
			}

		case strings.HasSuffix(nameLower, "_min"), nameLower == "min":
			mergedField.Set(smallerOf(mergedField, otherField))

		case strings.HasSuffix(nameLower, "_max"), nameLower == "max":
			mergedField.Set(largerOf(mergedField, otherField))

		default:
			if mergedField.IsZero() {
				mergedField.Set(otherField)
			}
		}
	}
}

func smallerOf(a, b reflect.Value) reflect.Value {
	switch a.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if a.Int() <= b.Int() {
			return a
		}
	case reflect.Float32, reflect.Float64:
		if a.Float() <= b.Float() {
			return a
		}
	}
	return b
}

func largerOf(a, b reflect.Value) reflect.Value {
	switch a.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if a.Int() >= b.Int() {
			return a
		}
	case reflect.Float32, reflect.Float64:
		if a.Float() >= b.Float() {
			return a
		}
	}
	return b
}

// applyGroupByMerge 合并带 GROUP BY 的分片聚合结果。
// 按 groupBy 字段值做 key，相同 key 的行调用 mergeRow 合并。
func applyGroupByMerge(merged reflect.Value, shardResults []reflect.Value, groupBy string) error {
	elemType := merged.Type().Elem()
	groupKeys := strings.Split(groupBy, ",")
	for i := range groupKeys {
		groupKeys[i] = strings.TrimSpace(groupKeys[i])
	}

	type keyRow struct {
		key  string
		elem reflect.Value
	}
	keyMap := make(map[string]reflect.Value)

	for _, r := range shardResults {
		if !r.IsValid() {
			continue
		}
		for i := 0; i < r.Len(); i++ {
			row := r.Index(i)
			key := buildGroupKey(row, groupKeys)
			if existing, ok := keyMap[key]; ok {
				mergeRow(existing, row)
			} else {
				cp := reflect.New(elemType).Elem()
				cp.Set(row)
				keyMap[key] = cp
			}
		}
	}

	out := reflect.MakeSlice(merged.Type(), 0, len(keyMap))
	for _, v := range keyMap {
		out = reflect.Append(out, v)
	}
	merged.Set(out)
	return nil
}

func buildGroupKey(row reflect.Value, groupKeys []string) string {
	var b strings.Builder
	for i, gk := range groupKeys {
		if i > 0 {
			b.WriteByte('|')
		}
		camel := toCamelCase(gk)
		f := row.FieldByName(camel)
		if !f.IsValid() {
			b.WriteString(gk)
			continue
		}
		switch f.Kind() {
		case reflect.String:
			b.WriteString(f.String())
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			fmt.Fprintf(&b, "%d", f.Int())
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			fmt.Fprintf(&b, "%d", f.Uint())
		case reflect.Bool:
			if f.Bool() {
				b.WriteByte('1')
			} else {
				b.WriteByte('0')
			}
		default:
			b.WriteString(fmt.Sprintf("%v", f.Interface()))
		}
	}
	return b.String()
}

// Sum 便捷聚合: SELECT SUM(column) AS <column>_sum FROM ...
// dest 是 *int64 或 *float64 等。分片自动求和。
func (qb *QueryBuilder) Sum(column string, dest interface{}) error {
	if qb.groupBy != "" {
		return errors.New("Sum 不支持 GROUP BY，请改用 Aggregate 方法")
	}
	alias := toValidAlias(column) + "_sum"
	type row struct {
		Sum interface{} `gorm:"column:"`
	}
	_ = row{}
	// 用通用 Aggregate
	var rows []struct {
		Sum float64 `gorm:"column:"`
	}
	if err := qb.Aggregate(&rows, fmt.Sprintf("SUM(%s) AS %s", column, alias)); err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	destVal := reflect.ValueOf(dest)
	if destVal.Kind() != reflect.Ptr {
		return errors.New("dest 必须是指针")
	}
	switch destVal.Elem().Kind() {
	case reflect.Int, reflect.Int64, reflect.Int32:
		destVal.Elem().SetInt(int64(rows[0].Sum))
	case reflect.Float64, reflect.Float32:
		destVal.Elem().SetFloat(rows[0].Sum)
	default:
		return fmt.Errorf("不支持的 dest 类型: %v", destVal.Elem().Kind())
	}
	return nil
}

// Min 便捷聚合: SELECT MIN(column) AS <column>_min FROM ...
// 分片自动取最小。
func (qb *QueryBuilder) Min(column string, dest interface{}) error {
	return qb.scalarAggregate(column, dest, "MIN")
}

// Max 便捷聚合: SELECT MAX(column) AS <column>_max FROM ...
// 分片自动取最大。
func (qb *QueryBuilder) Max(column string, dest interface{}) error {
	return qb.scalarAggregate(column, dest, "MAX")
}

// Avg 便捷聚合: 分片场景下使用全局加权平均 = SUM(column) / SUM(*)
// 这比直接 avg(avg1, avg2, avg3) 更精确（后者是错误的，分片行数不同时结果不对）。
// 内部同时跑 SUM(column) 和 COUNT(*)，mergeRow 自动累加两者，最后相除。
func (qb *QueryBuilder) Avg(column string, dest interface{}) error {
	destVal := reflect.ValueOf(dest)
	if destVal.Kind() != reflect.Ptr {
		return errors.New("dest 必须是指针")
	}

	if qb.groupBy == "" {
		var rows []struct {
			Sum float64 `gorm:"column:sum_value"`
			Cnt int64   `gorm:"column:cnt"`
		}
		if err := qb.Aggregate(&rows,
			fmt.Sprintf("SUM(%s) AS sum_value, COUNT(*) AS cnt", column)); err != nil {
			return err
		}
		if len(rows) == 0 || rows[0].Cnt == 0 {
			return nil
		}
		avg := rows[0].Sum / float64(rows[0].Cnt)
		switch destVal.Elem().Kind() {
		case reflect.Int, reflect.Int64, reflect.Int32:
			destVal.Elem().SetInt(int64(avg))
		case reflect.Float64, reflect.Float32:
			destVal.Elem().SetFloat(avg)
		default:
			return fmt.Errorf("不支持的 dest 类型: %v", destVal.Elem().Kind())
		}
		return nil
	}

	// 有 GROUP BY: 每个 key 单独加权
	keyRows, err := qb.avgGroupBy(column)
	if err != nil {
		return err
	}
	destElemType := destVal.Elem().Type().Elem()
	merged := reflect.MakeSlice(destVal.Elem().Type(), 0, len(keyRows))
	for _, row := range keyRows {
		elem := reflect.New(destElemType).Elem()
		// 拷贝 GROUP BY key 字段 + 设置 avg 值到合适字段
		copyGroupByFields(elem, row.GroupKey, qb.groupBy)
		setAvgField(elem, row.AvgValue)
		merged = reflect.Append(merged, elem)
	}
	destVal.Elem().Set(merged)
	return nil
}

type avgGroupRow struct {
	GroupKey map[string]interface{}
	SumValue float64
	Cnt      int64
	AvgValue float64
}

func (qb *QueryBuilder) avgGroupBy(column string) ([]avgGroupRow, error) {
	groupKeys := strings.Split(qb.groupBy, ",")
	for i := range groupKeys {
		groupKeys[i] = strings.TrimSpace(groupKeys[i])
	}
	selectExpr := strings.Join(groupKeys, ", ") + ", " +
		fmt.Sprintf("SUM(%s) AS %s_sum, COUNT(*) AS cnt", column, toValidAlias(column))

	type outRow struct {
		Sum float64 `gorm:"column:sum_value"`
		Cnt int64   `gorm:"column:cnt"`
	}
	var rows []outRow
	if err := qb.Aggregate(&rows, selectExpr); err != nil {
		return nil, err
	}

	var out []avgGroupRow
	for _, r := range rows {
		avg := 0.0
		if r.Cnt > 0 {
			avg = r.Sum / float64(r.Cnt)
		}
		out = append(out, avgGroupRow{
			SumValue: r.Sum,
			Cnt:      r.Cnt,
			AvgValue: avg,
		})
	}
	return out, nil
}

// copyGroupByFields 从 source 的 GROUP BY key 值拷贝到 target elem
func copyGroupByFields(target reflect.Value, groupKey map[string]interface{}, groupBy string) {
	keys := strings.Split(groupBy, ",")
	for i, k := range keys {
		k = strings.TrimSpace(k)
		camel := toCamelCase(k)
		f := target.FieldByName(camel)
		if !f.IsValid() {
			continue
		}
		if v, ok := groupKey[k]; ok {
			rv := reflect.ValueOf(v)
			if rv.IsValid() && f.Kind() == rv.Kind() {
				f.Set(rv)
			}
		}
		_ = i
	}
}

// setAvgField 找到 target 中包含 avg/Average/Mean 的字段，设置为 val
func setAvgField(target reflect.Value, val float64) {
	t := target.Type()
	for i := 0; i < t.NumField(); i++ {
		nameLower := strings.ToLower(t.Field(i).Name)
		if strings.Contains(nameLower, "avg") ||
			strings.Contains(nameLower, "average") ||
			strings.Contains(nameLower, "mean") {
			f := target.Field(i)
			switch f.Kind() {
			case reflect.Float32, reflect.Float64:
				f.SetFloat(val)
			case reflect.Int, reflect.Int64:
				f.SetInt(int64(val))
			}
			return
		}
	}
}

// scalarAggregate 通用单值聚合内部实现
func (qb *QueryBuilder) scalarAggregate(column string, dest interface{}, fn string) error {
	if qb.groupBy != "" {
		return fmt.Errorf("%s 不支持 GROUP BY，请改用 Aggregate 方法", fn)
	}
	alias := toValidAlias(column) + "_" + strings.ToLower(fn)
	var rows []struct {
		Val float64 `gorm:"column:"`
	}
	if err := qb.Aggregate(&rows, fmt.Sprintf("%s(%s) AS %s", fn, column, alias)); err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	destVal := reflect.ValueOf(dest)
	if destVal.Kind() != reflect.Ptr {
		return errors.New("dest 必须是指针")
	}
	switch destVal.Elem().Kind() {
	case reflect.Int, reflect.Int64, reflect.Int32:
		destVal.Elem().SetInt(int64(rows[0].Val))
	case reflect.Float64, reflect.Float32:
		destVal.Elem().SetFloat(rows[0].Val)
	default:
		return fmt.Errorf("不支持的 dest 类型: %v", destVal.Elem().Kind())
	}
	return nil
}

func toValidAlias(column string) string {
	return strings.ReplaceAll(column, ".", "_")
}

// Exists 检查是否有任何分片命中条件。
// 优化: 逐分片 SELECT 1 LIMIT 1，找到即返回 true —— 不扫全部分片。
// 如果设置了 ctx.Done() 会提前返回。
func (qb *QueryBuilder) Exists() (bool, error) {
	if len(qb.tables) == 0 {
		return false, errors.New("未指定查询表")
	}
	if err := qb.validateTables(); err != nil {
		return false, err
	}

	var lastErr error
	for _, table := range qb.tables {
		select {
		case <-qb.ctx.Done():
			return false, qb.ctx.Err()
		default:
		}
		tableQB := qb.cloneForTable(qb.ctx, table, true)
		var cnt int64
		err := tableQB.buildSingleTableQuery().Count(&cnt).Error
		if err != nil {
			lastErr = err
			continue
		}
		if cnt > 0 {
			return true, nil
		}
	}
	if lastErr != nil {
		return false, fmt.Errorf("Exists 查询分片失败: %w", lastErr)
	}
	return false, nil
}

// Scan 扫描查询结果到 dest（可以是 struct slice、map slice、map[string]interface{} 等）。
// 语义与 GORM Scan 一致，但自动处理多分片 UNION ALL 或并行聚合。
// 当只有一个分片时直接走单表查询，多分片时：
//   - 无 GROUP BY / 无 ORDER BY / 无 LIMIT → 并行各分片 Scan 再合并
//   - 有上述条件 → 走 UNION ALL（保证排序/分页语义正确）
func (qb *QueryBuilder) Scan(dest interface{}) error {
	if len(qb.tables) == 0 {
		return errors.New("未指定查询表")
	}
	if err := qb.validateTables(); err != nil {
		return err
	}

	if len(qb.tables) == 1 {
		return qb.buildSingleTableQuery().Scan(dest).Error
	}

	// 多分片判断走哪条路径
	needUnion := qb.groupBy != "" || qb.orderBy != "" || qb.limit > 0 || qb.offset > 0

	if needUnion {
		query, err := qb.buildUnionAllQuery()
		if err != nil {
			return err
		}
		return query.Scan(dest).Error
	}

	// 并行各分片 Scan 再合并
	destValue := reflect.ValueOf(dest)
	if destValue.Kind() != reflect.Ptr || destValue.Elem().Kind() != reflect.Slice {
		return errors.New("dest 必须是指向切片的指针")
	}
	elemType := destValue.Elem().Type().Elem()

	results, err := parallelQuery(qb.ctx, qb.tables, 0, func(ctx context.Context, table string) (reflect.Value, error) {
		tableQB := qb.cloneForTable(ctx, table, true)
		sliceType := reflect.SliceOf(elemType)
		tempSlice := reflect.New(sliceType).Interface()
		if err := tableQB.buildSingleTableQuery().Scan(tempSlice).Error; err != nil {
			return reflect.Value{}, fmt.Errorf("Scan 表 %s 失败: %w", table, err)
		}
		return reflect.ValueOf(tempSlice).Elem(), nil
	})
	if err != nil {
		return err
	}

	merged := reflect.MakeSlice(destValue.Elem().Type(), 0, 0)
	for _, r := range results {
		if r.IsValid() && r.Len() > 0 {
			merged = reflect.AppendSlice(merged, r)
		}
	}
	destValue.Elem().Set(merged)
	return nil
}

// FindInBatches 大结果集分批迭代器。
// 按分片顺序，每个分片按 batchSize 分页拉取，直到所有分片处理完毕。
// callback 接收一批数据；返回 false 或 ctx 取消时提前终止。
// 这避免一次性把所有分片的结果加载到内存（FindParallel 会这样）。
//
// 注意: batchSize 必须 > 0。不支持 ORDER BY（分片内分批无全局排序语义）。
func (qb *QueryBuilder) FindInBatches(dest interface{}, batchSize int, callback func(batch int64) bool) error {
	if len(qb.tables) == 0 {
		return errors.New("未指定查询表")
	}
	if batchSize <= 0 {
		return errors.New("batchSize 必须 > 0")
	}
	if qb.orderBy != "" {
		return errors.New("FindInBatches 不支持 ORDER BY（分片内分批无法保证全局排序语义）")
	}
	if qb.groupBy != "" {
		return errors.New("FindInBatches 不支持 GROUP BY")
	}
	if err := qb.validateTables(); err != nil {
		return err
	}

	destValue := reflect.ValueOf(dest)
	if destValue.Kind() != reflect.Ptr || destValue.Elem().Kind() != reflect.Slice {
		return errors.New("dest 必须是指向切片的指针")
	}
	elemType := destValue.Elem().Type().Elem()

	var globalBatch int64

	for _, table := range qb.tables {
		select {
		case <-qb.ctx.Done():
			return qb.ctx.Err()
		default:
		}

		tableQB := qb.cloneForTable(qb.ctx, table, true)

		for offset := 0; ; offset += batchSize {
			select {
			case <-qb.ctx.Done():
				return qb.ctx.Err()
			default:
			}

			sliceType := reflect.SliceOf(elemType)
			batchPtr := reflect.New(sliceType).Interface()

			err := tableQB.buildSingleTableQuery().Limit(batchSize).Offset(offset).Find(batchPtr).Error
			if err != nil {
				return fmt.Errorf("分批查询表 %s offset=%d 失败: %w", table, offset, err)
			}

			batchVal := reflect.ValueOf(batchPtr).Elem()
			if batchVal.Len() == 0 {
				break
			}

			destValue.Elem().Set(batchVal)

			globalBatch++
			if !callback(globalBatch) {
				return nil
			}

			if batchVal.Len() < batchSize {
				break
			}
		}
	}
	return nil
}

// RawBuilder 封装原始 SQL + {table} 占位符的分片路由执行器。
// 用 QueryBuilder.Raw() 创建。
type RawBuilder struct {
	qb   *QueryBuilder
	sql  string
	args []interface{}
}

// Raw 创建原始 SQL 分片路由执行器。
//
// sql 中可以使用 `{table}` 占位符，执行时会被替换为每个分片表的真实表名。
// 也可以写死表名（不走自动替换），这时只会在第一个分片上执行 —— 不推荐。
//
// 执行模式:
//   - ScanRaw / ExecRaw: 并行执行所有分片，合并结果
//   - FirstRaw: 逐分片执行，命中即返回
//   - ExecRaw: 并行执行写操作（UPDATE/DELETE/INSERT），汇总影响行数
//
// Example:
//
//	qb := NewQueryBuilder(db, rule).TimeRange(...).Where("status = ?", 4)
//	var rows []Order
//	qb.Raw("SELECT * FROM {table} WHERE status = ? AND amount > (SELECT AVG(amount) FROM {table})", 4).ScanRaw(&rows)
//
//	// 写操作也支持
//	res, _ := qb.Raw("UPDATE {table} SET status = ? WHERE expired_at < NOW()", 0).ExecRaw()
func (qb *QueryBuilder) Raw(sql string, args ...interface{}) *RawBuilder {
	assertPlaceholderCount(sql, args)
	return &RawBuilder{qb: qb, sql: sql, args: args}
}

// ScanRaw 并行执行所有分片的 Raw SQL，合并 Scan 结果到 dest。
// 合并策略与 Scan 方法相同：有 GROUP BY/ORDER BY/LIMIT → UNION ALL 路径；无 → 并行各分片再 append。
func (rb *RawBuilder) ScanRaw(dest interface{}) error {
	if len(rb.qb.tables) == 0 {
		return errors.New("未指定查询表")
	}
	if err := rb.qb.validateTables(); err != nil {
		return err
	}

	destValue := reflect.ValueOf(dest)
	if destValue.Kind() != reflect.Ptr || destValue.Elem().Kind() != reflect.Slice {
		return errors.New("dest 必须是指向切片的指针")
	}
	elemType := destValue.Elem().Type().Elem()

	results, err := parallelQuery(rb.qb.ctx, rb.qb.tables, 0, func(ctx context.Context, table string) (reflect.Value, error) {
		actualSQL := strings.ReplaceAll(rb.sql, "{table}", table)
		db := rb.qb.db
		if ctx != nil {
			db = db.WithContext(ctx)
		}
		sliceType := reflect.SliceOf(elemType)
		tempSlice := reflect.New(sliceType).Interface()
		if err := db.Raw(actualSQL, rb.args...).Scan(tempSlice).Error; err != nil {
			return reflect.Value{}, fmt.Errorf("Raw Scan 表 %s 失败: %w", table, err)
		}
		return reflect.ValueOf(tempSlice).Elem(), nil
	})
	if err != nil {
		return err
	}

	merged := reflect.MakeSlice(destValue.Elem().Type(), 0, 0)
	for _, r := range results {
		if r.IsValid() && r.Len() > 0 {
			merged = reflect.AppendSlice(merged, r)
		}
	}
	destValue.Elem().Set(merged)
	return nil
}

// FirstRaw 逐分片执行 Raw SQL，返回第一条命中记录。
// 命中即返回，不必扫完全部分片。
func (rb *RawBuilder) FirstRaw(dest interface{}) error {
	if len(rb.qb.tables) == 0 {
		return errors.New("未指定查询表")
	}
	if err := rb.qb.validateTables(); err != nil {
		return err
	}

	var lastErr error
	for _, table := range rb.qb.tables {
		select {
		case <-rb.qb.ctx.Done():
			return rb.qb.ctx.Err()
		default:
		}
		actualSQL := strings.ReplaceAll(rb.sql, "{table}", table)
		db := rb.qb.db.WithContext(rb.qb.ctx)
		if err := db.Raw(actualSQL, rb.args...).Scan(dest).Error; err != nil {
			lastErr = err
			continue
		}
		destVal := reflect.ValueOf(dest)
		if destVal.Kind() == reflect.Ptr {
			destVal = destVal.Elem()
		}
		if destVal.Kind() == reflect.Slice && destVal.Len() > 0 {
			return nil
		}
		if destVal.Kind() != reflect.Slice {
			if !destVal.IsZero() {
				return nil
			}
		}
	}
	if lastErr != nil {
		return lastErr
	}
	return gorm.ErrRecordNotFound
}

// ExecRaw 并行执行写操作（UPDATE/DELETE/INSERT），汇总所有分片的影响行数。
// 适用于需要跨分片统一更新/清理的场景。
func (rb *RawBuilder) ExecRaw() (int64, error) {
	if len(rb.qb.tables) == 0 {
		return 0, errors.New("未指定查询表")
	}
	if err := rb.qb.validateTables(); err != nil {
		return 0, err
	}

	affected, err := parallelQuery(rb.qb.ctx, rb.qb.tables, 0, func(ctx context.Context, table string) (int64, error) {
		actualSQL := strings.ReplaceAll(rb.sql, "{table}", table)
		db := rb.qb.db.WithContext(ctx)
		res := db.Exec(actualSQL, rb.args...)
		if res.Error != nil {
			return 0, fmt.Errorf("Raw Exec 表 %s 失败: %w", table, res.Error)
		}
		return res.RowsAffected, nil
	})
	if err != nil {
		return 0, err
	}

	var total int64
	for _, a := range affected {
		total += a
	}
	return total, nil
}

// CreateInBatches 分片批量写入。
//
// records 是一个指向切片的指针（*[]Order 等），batchSize 是每批 INSERT 的大小。
// 方法会:
//  1. 按 ShardRule 把 records 中的每条记录路由到正确的分片表
//  2. 对每个分片表，按 batchSize 分批 INSERT
//  3. 返回所有分片总共插入的行数
//
// 依赖: ShardRule 必须能拿到 records 的分片键。
//   - ByIDHash / ByIDRange: 需要 records 有一个 int64 类型的主键字段（支持索尼雪花 ID）
//   - ByYear / ByMonth / ByWeek / ByTenDay: 需要 records 有 time.Time 类型的时间字段
//
// 使用示例:
//
//	var orders []Order
//	for i := 0; i < 10000; i++ {
//	    orders = append(orders, Order{ID: sonyflake.NextID(), CreatedAt: time.Now(), ...})
//	}
//	affected, err := qb.CreateInBatches(&orders, 500)
func (qb *QueryBuilder) CreateInBatches(records interface{}, batchSize int) (int64, error) {
	if qb.shardRule == nil {
		return 0, errors.New("CreateInBatches 需要绑定 ShardRule")
	}
	if batchSize <= 0 {
		return 0, errors.New("batchSize 必须 > 0")
	}

	recordsVal := reflect.ValueOf(records)
	if recordsVal.Kind() != reflect.Ptr || recordsVal.Elem().Kind() != reflect.Slice {
		return 0, errors.New("records 必须是指向切片的指针")
	}
	slice := recordsVal.Elem()
	elemType := slice.Type().Elem()

	if slice.Len() == 0 {
		return 0, nil
	}

	buckets := make(map[string]reflect.Value)
	for i := 0; i < slice.Len(); i++ {
		elem := slice.Index(i)
		table, err := qb.routeRecord(elem, elemType)
		if err != nil {
			return 0, fmt.Errorf("路由第 %d 条记录失败: %w", i, err)
		}
		if _, ok := buckets[table]; !ok {
			buckets[table] = reflect.MakeSlice(slice.Type(), 0, 0)
		}
		buckets[table] = reflect.Append(buckets[table], elem)
	}

	var totalAffected int64
	for table, bucket := range buckets {
		if bucket.Len() == 0 {
			continue
		}
		for offset := 0; offset < bucket.Len(); offset += batchSize {
			end := offset + batchSize
			if end > bucket.Len() {
				end = bucket.Len()
			}
			batchSlice := bucket.Slice(offset, end)
			batchPtr := reflect.New(batchSlice.Type())
			batchPtr.Elem().Set(batchSlice)

			db := qb.db.WithContext(qb.ctx).Table(table)
			res := db.Create(batchPtr.Interface())
			if res.Error != nil {
				return totalAffected, fmt.Errorf("分片表 %s 批量 INSERT 失败: %w", table, res.Error)
			}
			totalAffected += res.RowsAffected
		}
	}
	return totalAffected, nil
}

func (qb *QueryBuilder) routeRecord(elem reflect.Value, elemType reflect.Type) (string, error) {
	rule := qb.shardRule
	switch rule.Type {
	case ByYear, ByQuarter, ByMonth, ByWeek, ByTenDay:
		t, err := extractTimeField(elem, elemType)
		if err != nil {
			return "", err
		}
		return rule.GetTableNameByTime(t)

	case ByIDHash, ByIDRange:
		id, err := extractInt64Field(elem, elemType)
		if err != nil {
			return "", err
		}
		return rule.GetTableNameByID(id)

	default:
		return "", fmt.Errorf("不支持的分片类型: %s", rule.Type)
	}
}

func extractTimeField(elem reflect.Value, elemType reflect.Type) (time.Time, error) {
	for _, name := range []string{"CreatedAt", "UpdatedAt", "created_at", "updated_at"} {
		f := elem.FieldByName(name)
		if !f.IsValid() {
			f = elem.FieldByName(toCamelCase(name))
		}
		if f.IsValid() {
			if t, ok := f.Interface().(time.Time); ok {
				return t, nil
			}
		}
	}
	for i := 0; i < elemType.NumField(); i++ {
		f := elem.Field(i)
		if f.Kind() == reflect.Struct {
			if t, ok := f.Interface().(time.Time); ok {
				return t, nil
			}
		}
	}
	return time.Time{}, errors.New("无法从记录中提取 time.Time 字段（需要 CreatedAt/UpdatedAt 等）")
}

func extractInt64Field(elem reflect.Value, elemType reflect.Type) (int64, error) {
	for _, name := range []string{"ID", "Id", "id"} {
		f := elem.FieldByName(name)
		if f.IsValid() && (f.Kind() == reflect.Int64 || f.Kind() == reflect.Int) {
			return f.Int(), nil
		}
	}
	for i := 0; i < elemType.NumField(); i++ {
		f := elem.Field(i)
		fnameLower := strings.ToLower(elemType.Field(i).Name)
		if fnameLower == "id" && (f.Kind() == reflect.Int64 || f.Kind() == reflect.Int) {
			return f.Int(), nil
		}
	}
	return 0, errors.New("无法从记录中提取 int64 ID 字段（需要 ID/Id/id 等）")
}