package sharding

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"golang.org/x/sync/errgroup"
	"gorm.io/gorm"
)

// QueryBuilder 用于构建和执行跨分片查询
type QueryBuilder struct {
	db            *gorm.DB
	shardRule     *ShardRule
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
}

// NewQueryBuilder 创建新的查询构建器
func NewQueryBuilder(db *gorm.DB, rule *ShardRule) *QueryBuilder {
	return &QueryBuilder{
		db:        db,
		shardRule: rule,
		tables:    make([]string, 0),
	}
}

// Tables 设置要查询的表名，支持传入单个表名或多个表名（会自动拼接为 FROM a, b 的形式）
// 示例:
// qb.Tables("users")
// qb.Tables("users u", "orders o")
func (qb *QueryBuilder) Tables(tables ...string) *QueryBuilder {
	qb.tables = append(qb.tables, tables...)
	return qb
}

// Select 指定要查询的列，可以传入多个字段或表达式
// 示例:
// qb.Select("auto_id", "name", "age")
// qb.Select("COUNT(*) as count")
// qb.Select("u.auto_id", "o.order_no", "SUM(o.amount) as total_amount")
func (qb *QueryBuilder) Select(columns ...string) *QueryBuilder {
	qb.selectColumns = columns
	return qb
}

// Where 添加查询条件
// 示例:
// qb.Where("status = ?", 4)
// qb.Where("created_at >= ?", "2025-08-01 00:00:00").Where("created_at <= ?", "2025-08-26 00:00:00")
// => WHERE (status = ?) AND (created_at >= ?) AND (created_at <= ?)
func (qb *QueryBuilder) Where(query string, args ...interface{}) *QueryBuilder {
	if qb.whereClause == "" {
		qb.whereClause = query
	} else {
		qb.whereClause = fmt.Sprintf("(%s) AND (%s)", qb.whereClause, query)
	}
	qb.args = append(qb.args, args...)
	return qb
}

// OrWhere 添加 OR 条件
// 示例:
// qb.Where("status = ?", 4).OrWhere("status = ?", 5)
// qb.Where("created_at < ?", "2025-08-01").OrWhere("created_at > ?", "2025-08-26")
// => WHERE (status = ?) OR (status = ?)
func (qb *QueryBuilder) OrWhere(query string, args ...interface{}) *QueryBuilder {
	if qb.whereClause == "" {
		return qb.Where(query, args...)
	}
	qb.whereClause = fmt.Sprintf("(%s) OR (%s)", qb.whereClause, query)
	qb.args = append(qb.args, args...)
	return qb
}

// Order 添加排序条件
// 示例:
// qb.Order("auto_id DESC")
// qb.Order("created_at ASC")
// => ORDER BY auto_id DESC
func (qb *QueryBuilder) Order(order string) *QueryBuilder {
	qb.orderBy = order
	return qb
}

// Group 添加分组
// 示例:
// qb.Group("status")
// qb.Group("status", "category")
// => GROUP BY status, category
func (qb *QueryBuilder) Group(columns ...string) *QueryBuilder {
	qb.groupBy = strings.Join(columns, ", ")
	return qb
}

// Having 添加 HAVING 条件
// 示例:
// qb.Group("status").Having("COUNT(auto_id) > ?", 10)
// => GROUP BY status HAVING COUNT(auto_id) > 10
func (qb *QueryBuilder) Having(having string, args ...interface{}) *QueryBuilder {
	qb.having = having
	qb.havingArgs = args
	return qb
}

// Limit 设置查询返回的最大记录数
// 示例:
// qb.Limit(10)   // 只返回前 10 条记录
func (qb *QueryBuilder) Limit(limit int) *QueryBuilder {
	qb.limit = limit
	return qb
}

// Offset 设置查询的偏移量（用于分页）
// 示例:
// qb.Offset(20)  // 从第 21 条记录开始查询
func (qb *QueryBuilder) Offset(offset int) *QueryBuilder {
	qb.offset = offset
	return qb
}

// buildSingleTableQuery 构建单表查询
// 根据 QueryBuilder 中设置的表名、字段、条件等生成 gorm.DB 查询对象
// 使用示例:
// qb := NewQueryBuilder(db).
//
//	Tables("users").
//	Select("auto_id", "name").
//	Where("age > ?", 18).
//	Order("auto_id DESC").
//	Limit(10).
//	Offset(20)
//
// query := qb.buildSingleTableQuery()
// var results []User
// query.Find(&results)
func (qb *QueryBuilder) buildSingleTableQuery() *gorm.DB {
	query := qb.db.Table(qb.tables[0])

	// 添加 SELECT 子句
	if len(qb.selectColumns) > 0 {
		query = query.Select(qb.selectColumns)
	}

	// 添加 WHERE 条件
	if qb.whereClause != "" {
		query = query.Where(qb.whereClause, qb.args...)
	}

	// 添加 GROUP BY
	if qb.groupBy != "" {
		query = query.Group(qb.groupBy)
	}

	// 添加 HAVING
	if qb.having != "" {
		query = query.Having(qb.having)
	}

	// 添加 ORDER BY
	if qb.orderBy != "" {
		query = query.Order(qb.orderBy)
	}

	// 添加 LIMIT 和 OFFSET
	if qb.limit > 0 {
		query = query.Limit(qb.limit)
	}
	if qb.offset > 0 {
		query = query.Offset(qb.offset)
	}

	return query
}

// buildUnionAllQuery 构建多表 UNION ALL 查询（支持分页和参数绑定）
// 该方法将多个表的查询结果合并为一个结果集，适用于需要从多个结构相同的表中查询数据的场景
//
// 工作流程：
// 1. 检查表数量，至少需要两个表才能进行 UNION ALL 操作
// 2. 构建 SELECT 子句，支持自定义查询字段
// 3. 为每个表构建子查询，包括 WHERE、GROUP BY 和 HAVING 条件
// 4. 使用 UNION ALL 连接所有子查询，并包装在外部 SELECT 中
// 5. 添加 ORDER BY、LIMIT 和 OFFSET 子句到外部查询
// 6. 使用 gorm.Raw 执行原始 SQL 查询
//
// 参数绑定说明：
// - WHERE 条件使用 qb.args 中的参数
// - HAVING 条件使用 qb.havingArgs 中的参数
// - LIMIT 和 OFFSET 会自动添加到查询末尾
//
// 返回：
// - *gorm.DB: 包含 UNION ALL 查询的 gorm 查询对象
// - error: 如果表数量不足或构建查询时发生错误
//
// 示例 SQL 输出：
// SELECT * FROM (
//
//	(SELECT * FROM table1 WHERE status = ?)
//	UNION ALL
//	(SELECT * FROM table2 WHERE status = ?)
//
// ) AS union_result
// ORDER BY created_at DESC LIMIT 10 OFFSET 0
func (qb *QueryBuilder) buildUnionAllQuery() (*gorm.DB, error) {
	if len(qb.tables) < 2 {
		return nil, errors.New("至少需要两个表进行 UNION ALL 查询")
	}

	var unionQueries []string
	var unionArgs []interface{}

	// 构建 SELECT 子句
	selectClause := "*"
	if len(qb.selectColumns) > 0 {
		selectClause = strings.Join(qb.selectColumns, ", ")
	}

	// 为每个表构建子查询
	for _, table := range qb.tables {
		subQuery := fmt.Sprintf("SELECT %s FROM %s", selectClause, table)

		// WHERE 条件
		if qb.whereClause != "" {
			subQuery += " WHERE " + qb.whereClause
			unionArgs = append(unionArgs, qb.args...) // 使用 qb.args 绑定
		}

		// GROUP BY
		if qb.groupBy != "" {
			subQuery += " GROUP BY " + qb.groupBy
		}

		// HAVING 条件（可单独传参）
		if qb.having != "" {
			subQuery += " HAVING " + qb.having
			unionArgs = append(unionArgs, qb.havingArgs...) // 需要单独维护
		}

		unionQueries = append(unionQueries, subQuery)
	}

	// 构建 UNION ALL 查询并包装在外部 SELECT 中
	unionSQL := "SELECT * FROM (\n"
	unionSQL += "  (" + strings.Join(unionQueries, ") UNION ALL\n   (") + ")\n"
	unionSQL += ") AS union_result"

	// 添加外部 ORDER BY
	if qb.orderBy != "" {
		unionSQL += "\nORDER BY " + qb.orderBy
	}

	// 添加外部 LIMIT 和 OFFSET
	if qb.limit > 0 {
		unionSQL += "\nLIMIT ?"
		unionArgs = append(unionArgs, qb.limit)
	}
	if qb.offset > 0 {
		unionSQL += " OFFSET ?"
		unionArgs = append(unionArgs, qb.offset)
	}

	// 执行原始 SQL 查询
	return qb.db.Raw(unionSQL, unionArgs...), nil
}

// validateTables 对配置的表名做白名单校验，防止通过 UNION ALL 等字符串拼接路径发生 SQL 注入。
// 支持 "表名 [别名]" 形式（如 "orders_1 o"），每个片段仅允许字母、数字与下划线。
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

// First 查询单条记录，如果找到多条记录，只返回第一条
// 该方法用于从单个表或多个表中查询单条记录，支持分表查询
//
// 工作流程：
// 1. 检查是否设置了查询表，未设置则返回错误
// 2. 如果是单表查询：
//   - 直接调用 buildSingleTableQuery 构建查询
//   - 添加 LIMIT 1 限制只返回一条记录
//
// 3. 如果是多表查询：
//   - 保存原始的 limit 设置
//   - 设置 limit = 1 确保只返回一条记录
//   - 调用 buildUnionAllQuery 构建 UNION ALL 查询
//   - 恢复原始的 limit 设置
//
// 4. 执行查询并将结果映射到 dest 参数
//
// 参数：
//   - dest: 用于存储查询结果的结构体指针
//
// 返回：
//   - error: 查询过程中发生的错误，包括：
//   - 未指定查询表
//   - 构建查询时出错
//   - 数据库查询错误
//
// 注意事项：
// - 如果查询结果为空，会返回 gorm.ErrRecordNotFound 错误
// - 多表查询时会自动处理分页，确保只返回一条记录
// - 会临时修改 limit 值，查询完成后会恢复原始值
//
// 使用示例：
// var user User
// err := NewQueryBuilder(db, shardRule).
//
//	Tables("users_2023", "users_2024").
//	Where("auto_id = ?", 1).
//	First(&user)
func (qb *QueryBuilder) First(dest interface{}) error {
	if len(qb.tables) == 0 {
		return errors.New("未指定查询表")
	}
	if err := qb.validateTables(); err != nil {
		return err
	}

	var query *gorm.DB
	var err error

	if len(qb.tables) == 1 {
		query = qb.buildSingleTableQuery().Limit(1)
	} else {
		// 保存原始 limit
		originalLimit := qb.limit
		qb.limit = 1
		query, err = qb.buildUnionAllQuery()
		if err != nil {
			return err
		}
		// 恢复原始 limit
		qb.limit = originalLimit
	}

	result := query.First(dest)
	return result.Error
}

// Find 查询多条记录，支持单表和多表查询
// 该方法用于从单个表或多个表中查询多条记录，支持分页、排序、条件筛选等功能
//
// 工作流程：
// 1. 检查是否设置了查询表，未设置则返回错误
// 2. 根据表数量选择查询方式：
//   - 单表：调用 buildSingleTableQuery 构建查询
//   - 多表：调用 buildUnionAllQuery 构建 UNION ALL 查询
//
// 3. 执行查询并将结果映射到 dest 参数
//
// 参数：
//   - dest: 用于存储查询结果的切片指针，例如 &[]User{}
//
// 返回：
//   - error: 查询过程中发生的错误，包括：
//   - 未指定查询表
//   - 构建查询时出错
//   - 数据库查询错误
//
// 注意事项：
// - 如果查询结果为空，不会返回错误，而是返回空切片
// - 多表查询时会自动处理分页和排序
// - 支持链式调用，可以组合使用 Where、Order、Limit 等方法
//
// 使用示例：
// // 查询单表
// var users []User
// err := NewQueryBuilder(db, shardRule).
//
//	Tables("users_2023").
//	Where("status = ?", "active").
//	Order("created_at DESC").
//	Limit(10).
//	Find(&users)
//
// // 多表查询
// var allUsers []User
// err := NewQueryBuilder(db, shardRule).
//
//	Tables("users_2023", "users_2024").
//	Select("auto_id", "name", "email").
//	Where("status = ?", "active").
//	Order("created_at DESC").
//	Find(&allUsers)
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

// FindParallel 并行查询多个表并在应用层合并结果
// 该方法会同时查询所有表，然后在内存中合并、排序和分页结果
//
// 工作流程：
// 1. 并行查询所有表
// 2. 收集所有结果到内存中
// 3. 根据排序条件对结果进行排序
// 4. 应用分页（offset 和 limit）
//
// 参数：
//   - dest: 用于存储查询结果的切片指针，例如 &[]User{}
//
// 返回：
//   - error: 查询过程中发生的错误
//
// 注意事项：
// - 此方法会查询所有表的所有匹配记录，可能会消耗较多内存
// - 适合数据量不大但需要全局排序和分页的场景
// - 支持链式调用，可以组合使用 Where、Order、Limit 等方法
//
// 使用示例：
// // 从多个表中并行查询并合并结果
// var users []User
// err := NewQueryBuilder(db, shardRule).
//
//	Tables("users_2023", "users_2024").
//	Where("status = ?", "active").
//	Order("created_at DESC").
//	Limit(20).
//	Offset(10).
//	FindParallel(&users)
func (qb *QueryBuilder) FindParallel(
	ctx context.Context,
	dest interface{},
	compareFunc func(i, j interface{}) bool,
) error {
	// 1. 校验参数
	destValue := reflect.ValueOf(dest)
	if destValue.Kind() != reflect.Ptr || destValue.Elem().Kind() != reflect.Slice {
		return errors.New("目标必须是指向切片的指针")
	}
	if err := qb.validateTables(); err != nil {
		return err
	}

	elemType := destValue.Elem().Type().Elem() // 例如 User

	// 2. 带取消的上下文
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// 3. 使用 errgroup
	g, ctx := errgroup.WithContext(ctx)

	// 结果临时容器：每个 goroutine 放一份
	results := make([]reflect.Value, len(qb.tables))

	for idx, table := range qb.tables {
		idx, table := idx, table
		g.Go(func() error {
			// 克隆 QueryBuilder，避免共享
			tableQB := qb.cloneForTable(ctx, table)

			// 临时切片容器
			sliceType := reflect.SliceOf(elemType)
			tempSlice := reflect.New(sliceType).Interface()

			// 执行查询
			if err := tableQB.buildSingleTableQuery().Find(tempSlice).Error; err != nil {
				return fmt.Errorf("查询表 %s 失败: %w", table, err)
			}

			results[idx] = reflect.ValueOf(tempSlice).Elem()
			return nil
		})
	}

	// 4. 等待所有查询完成
	if err := g.Wait(); err != nil {
		return err
	}

	// 5. 合并结果
	merged := reflect.MakeSlice(destValue.Elem().Type(), 0, 0)
	for _, r := range results {
		if r.IsValid() && r.Len() > 0 {
			merged = reflect.AppendSlice(merged, r)
		}
	}

	// 6. 排序
	if compareFunc != nil && merged.Len() > 1 {
		sort.Slice(merged.Interface(), func(i, j int) bool {
			return compareFunc(merged.Index(i).Interface(), merged.Index(j).Interface())
		})
	}

	// 7. 分页
	merged = qb.applyPagination(merged)

	// 8. 设置结果
	destValue.Elem().Set(merged)
	return nil
}

// cloneForTable 克隆 QueryBuilder 并绑定指定表
func (qb *QueryBuilder) cloneForTable(ctx context.Context, table string) *QueryBuilder {
	return &QueryBuilder{
		db:            qb.db.WithContext(ctx),
		shardRule:     qb.shardRule,
		selectColumns: append([]string{}, qb.selectColumns...),
		whereClause:   qb.whereClause,
		args:          append([]interface{}{}, qb.args...),
		groupBy:       qb.groupBy,
		having:        qb.having,
		havingArgs:    append([]interface{}{}, qb.havingArgs...),
		limit:         qb.limit,
		offset:        qb.offset,
		tables:        []string{table},
	}
}

// applyPagination 应用全局分页
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
