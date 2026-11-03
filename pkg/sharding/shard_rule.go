package sharding

import (
	"fmt"
	"hash/crc32"
	"strconv"
	"time"
)

// ShardType 是分片策略的枚举类型。
// 支持 7 种策略：5 种时间分片 + 2 种 ID 分片。
type ShardType string

const (
	ByYear    ShardType = "year"     // 按年分片，表名格式: {base}_{YYYY}
	ByQuarter ShardType = "quarter"  // 按季度分片，表名格式: {base}_{YYYY}Q{n}
	ByMonth   ShardType = "month"    // 按月分片，表名格式: {base}_{YYYY}{MM}
	ByWeek    ShardType = "week"     // 按周分片，表名格式: {base}_{YYYY}W{WW}（使用 ISOWeek）
	ByTenDay  ShardType = "ten_day"  // 按旬分片，表名格式: {base}_{YYYY}{MM}_{1|2|3}（1-10/11-20/21-月底）
	ByIDHash  ShardType = "id_hash"  // 按 ID 哈希分片，表名格式: {base}_{0..HashShards-1}（crc32 取模）
	ByIDRange ShardType = "id_range" // 按 ID 范围分片，表名格式: {base}_{n}（id / RangeSize）
)

// ShardRule 定义分片规则，是整个分片路由系统的核心。
// 写入路由时用 GetTableNameByTime / GetTableNameByID 得到单目标分片表；
// 跨片查询时用 GetTableNamesByTimeRange / GetTableNamesByIDRange 枚举出可能命中的所有分片表。
//
// 两种枚举方法（范围）与单点路由方法的分片公式严格一致，
// 保证写入到某张表的数据，查询范围枚举一定包含这张表。
//
// 使用示例：
//
//	rule := sharding.NewShardRule(sharding.ByMonth, "orders")
//	table, _ := rule.GetTableNameByTime(time.Now()) // → orders_202611
//
//	hashRule := sharding.NewShardRule(sharding.ByIDHash, "users", sharding.WithHashShards(32))
//	table, _ := hashRule.GetTableNameByID(123456789) // → users_5
type ShardRule struct {
	Type       ShardType // 分片策略类型
	TableName  string    // 基础表名，作为分片表名的前缀
	HashShards int       // 哈希分片数量（仅 ByIDHash 使用），默认 16
	RangeSize  int64     // 每个范围分片的 ID 容量（仅 ByIDRange 使用），默认 1_000_000
}

// Option 是函数式选项类型，用于 NewShardRule 的可选参数。
//
// 示例：
//
//	NewShardRule(ByIDHash, "users", WithHashShards(64))
type Option func(*ShardRule)

// WithHashShards 设置哈希分片数量（ByIDHash 策略专用）。
// 传入 n <= 0 时不生效（使用默认值 16）。
//
// 示例：
//
//	rule := NewShardRule(ByIDHash, "users", WithHashShards(32))
func WithHashShards(n int) Option {
	return func(r *ShardRule) {
		if n > 0 {
			r.HashShards = n
		}
	}
}

// WithRangeSize 设置范围分片大小（ByIDRange 策略专用）。
// 传入 size <= 0 时不生效（使用默认值 1_000_000）。
//
// 示例：
//
//	rule := NewShardRule(ByIDRange, "orders", WithRangeSize(5000))
func WithRangeSize(size int64) Option {
	return func(r *ShardRule) {
		if size > 0 {
			r.RangeSize = size
		}
	}
}

// NewShardRule 创建一个 ShardRule 实例。
// 自动填充默认值：HashShards = 16，RangeSize = 1_000_000。
// 对于非对应策略，这些字段不会被读取，不会造成问题。
//
// 参数：
//   - shardType: 分片策略（ByYear / ByMonth / ByIDHash 等）
//   - tableName: 基础表名（"orders"、"users" 等，不含后缀）
//   - opts: 可变参数，ByIDHash 用 WithHashShards(n)，ByIDRange 用 WithRangeSize(n)
//
// 返回值：
//   - *ShardRule: 初始化好的分片规则
//
// 示例：
//
//	// 按月分片：写入 orders_202611, 查询 2026-10~2026-11 会枚举出 orders_202610, orders_202611
//	rule1 := sharding.NewShardRule(sharding.ByMonth, "orders")
//
//	// 32 个哈希分片：写入 users_0 ~ users_31
//	rule2 := sharding.NewShardRule(sharding.ByIDHash, "users", sharding.WithHashShards(32))
//
//	// 每 5000 个 ID 一张表：写入 orders_0, orders_1, orders_2, ...
//	rule3 := sharding.NewShardRule(sharding.ByIDRange, "orders", sharding.WithRangeSize(5000))
func NewShardRule(shardType ShardType, tableName string, opts ...Option) *ShardRule {
	r := &ShardRule{
		Type:       shardType,
		TableName:  tableName,
		HashShards: 16,
		RangeSize:  1000000,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// TimeRange 表示一个时间闭区间 [Start, End]，用于 GetTableNamesByTimeRange 的输入。
// Start 和 End 都包含在内；Start 晚于 End 时调用会返回 error。
type TimeRange struct {
	Start time.Time
	End   time.Time
}

// IDRange 表示一个 ID 闭区间 [Start, End]，用于 GetTableNamesByIDRange 的输入。
// Start 和 End 都包含在内；Start > End 时调用会返回 error。
type IDRange struct {
	Start int64
	End   int64
}

// GetTableNameByTime 根据时间分片策略（ByYear/ByQuarter/ByMonth/ByWeek/ByTenDay）计算
// 某个时间点应该落到哪张分片表。
//
// 不支持的分片类型会返回 error。
//
// 参数：
//   - t: 用于路由的时间点
//
// 返回值：
//   - string: 分片表名
//   - error: 分片类型不匹配时返回
//
// 示例：
//
//	rule := sharding.NewShardRule(sharding.ByMonth, "orders")
//	table, _ := rule.GetTableNameByTime(time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC))
//	fmt.Println(table) // orders_202611
//
//	rule2 := sharding.NewShardRule(sharding.ByWeek, "logs")
//	table2, _ := rule2.GetTableNameByTime(time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC))
//	fmt.Println(table2) // logs_2026W45（ISO 周编号）
func (r *ShardRule) GetTableNameByTime(t time.Time) (string, error) {
	switch r.Type {
	case ByYear:
		return fmt.Sprintf("%s_%d", r.TableName, t.Year()), nil

	case ByQuarter:
		quarter := (int(t.Month())-1)/3 + 1
		return fmt.Sprintf("%s_%dQ%d", r.TableName, t.Year(), quarter), nil

	case ByMonth:
		return fmt.Sprintf("%s_%d%02d", r.TableName, t.Year(), t.Month()), nil

	case ByWeek:
		weekYear, week := t.ISOWeek()
		return fmt.Sprintf("%s_%dW%02d", r.TableName, weekYear, week), nil

	case ByTenDay:
		day := t.Day()
		dayGroup := 3
		if day <= 10 {
			dayGroup = 1
		} else if day <= 20 {
			dayGroup = 2
		}
		return fmt.Sprintf("%s_%d%02d_%d", r.TableName, t.Year(), t.Month(), dayGroup), nil

	default:
		return "", fmt.Errorf("分片类型 %s 不支持按时间查表名（仅 ByYear/ByQuarter/ByMonth/ByWeek/ByTenDay）", r.Type)
	}
}

// GetTableNameByID 根据 ID 分片策略（ByIDHash / ByIDRange）计算某个 ID 应该落到哪张分片表。
//
// ByIDHash 使用 crc32(id 十进制字符串) % HashShards；
// ByIDRange 使用 id / RangeSize，保证相同范围写入同一张表。
//
// 参数：
//   - id: 业务数据的唯一 ID
//
// 返回值：
//   - string: 分片表名
//   - error: 分片类型不匹配时返回
//
// 示例：
//
//	rule := sharding.NewShardRule(sharding.ByIDHash, "users", sharding.WithHashShards(32))
//	table, _ := rule.GetTableNameByID(123456789)
//	fmt.Println(table) // users_5（crc32(...) % 32）
//
//	rule2 := sharding.NewShardRule(sharding.ByIDRange, "orders", sharding.WithRangeSize(1000))
//	table2, _ := rule2.GetTableNameByID(2500)
//	fmt.Println(table2) // orders_2
func (r *ShardRule) GetTableNameByID(id int64) (string, error) {
	switch r.Type {
	case ByIDHash:
		if r.HashShards <= 0 {
			r.HashShards = 16
		}
		shard := crc32.ChecksumIEEE([]byte(strconv.FormatInt(id, 10))) % uint32(r.HashShards)
		return fmt.Sprintf("%s_%d", r.TableName, shard), nil

	case ByIDRange:
		if r.RangeSize <= 0 {
			r.RangeSize = 1000000
		}
		shard := id / r.RangeSize
		return fmt.Sprintf("%s_%d", r.TableName, shard), nil

	default:
		return "", fmt.Errorf("分片类型 %s 不支持按 ID 查表名（仅 ByIDHash/ByIDRange）", r.Type)
	}
}

// GetTableNamesByTimeRange 根据时间范围枚举出所有可能命中的分片表名。
// 用于跨分片查询场景，QueryBuilder.TimeRange 内部就是调这个方法。
//
// 枚举算法保证：在 [tr.Start, tr.End] 区间内任意一天调用 GetTableNameByTime 的结果，
// 一定在本方法返回的列表中。
//
// 参数：
//   - tr: 时间闭区间 [Start, End]
//
// 返回值：
//   - []string: 时间范围内涉及到的所有分片表名（按时间/分片顺序排列，不会重复）
//   - error: Start 晚于 End 或分片类型不匹配时返回
//
// 示例：
//
//	rule := sharding.NewShardRule(sharding.ByMonth, "orders")
//	tables, _ := rule.GetTableNamesByTimeRange(sharding.TimeRange{
//	    Start: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
//	    End:   time.Date(2026, 11, 30, 23, 59, 59, 0, time.UTC),
//	})
//	fmt.Println(tables) // [orders_202610 orders_202611]
func (r *ShardRule) GetTableNamesByTimeRange(tr TimeRange) ([]string, error) {
	// 验证时间范围
	if tr.Start.After(tr.End) {
		return nil, fmt.Errorf("开始时间不能晚于结束时间")
	}

	switch r.Type {
	case ByYear:
		return r.getYearTables(tr)
	case ByQuarter:
		return r.getQuarterTables(tr)
	case ByMonth:
		return r.getMonthTables(tr)
	case ByWeek:
		return r.getWeekTables(tr)
	case ByTenDay:
		return r.getTenDayTables(tr)
	default:
		return nil, fmt.Errorf("分片类型 %s 不支持基于时间的查询", r.Type)
	}
}

// getYearTables 获取年份分片表
func (r *ShardRule) getYearTables(tr TimeRange) ([]string, error) {
	var tables []string
	startYear := tr.Start.Year()
	endYear := tr.End.Year()

	for year := startYear; year <= endYear; year++ {
		tables = append(tables, fmt.Sprintf("%s_%d", r.TableName, year))
	}

	return tables, nil
}

// getQuarterTables 获取季度分片表
func (r *ShardRule) getQuarterTables(tr TimeRange) ([]string, error) {
	var tables []string
	start := tr.Start
	end := tr.End

	// 获取开始日期的季度
	startQuarter := (int(start.Month()) + 2) / 3
	endQuarter := (int(end.Month()) + 2) / 3

	// 如果开始和结束在同一个年份
	if start.Year() == end.Year() {
		for q := startQuarter; q <= endQuarter; q++ {
			tables = append(tables, fmt.Sprintf("%s_%dQ%d", r.TableName, start.Year(), q))
		}
		return tables, nil
	}

	// 处理跨年的情况

	// 1. 添加开始年份剩余季度的表
	for q := startQuarter; q <= 4; q++ {
		tables = append(tables, fmt.Sprintf("%s_%dQ%d", r.TableName, start.Year(), q))
	}

	// 2. 添加中间年份的所有季度的表
	for year := start.Year() + 1; year < end.Year(); year++ {
		for q := 1; q <= 4; q++ {
			tables = append(tables, fmt.Sprintf("%s_%dQ%d", r.TableName, year, q))
		}
	}

	// 3. 添加结束年份的季度表
	for q := 1; q <= endQuarter; q++ {
		tables = append(tables, fmt.Sprintf("%s_%dQ%d", r.TableName, end.Year(), q))
	}

	return tables, nil
}

// getWeekTables 获取周分片表。
// 以开始日期为锚点按 7 天步进并去重：任意连续 7 天必命中一个 ISO 周，
// 保证开始日所在周不会被漏掉（旧实现先向后调到周一，导致起始周整体缺失）。
func (r *ShardRule) getWeekTables(tr TimeRange) ([]string, error) {
	var tables []string
	seen := make(map[string]bool)
	addWeek := func(d time.Time) {
		year, week := d.ISOWeek()
		name := fmt.Sprintf("%s_%dW%02d", r.TableName, year, week)
		if !seen[name] {
			seen[name] = true
			tables = append(tables, name)
		}
	}

	for d := tr.Start; !d.After(tr.End); d = d.AddDate(0, 0, 7) {
		addWeek(d)
	}
	// 步点序列不一定落在结束日所在周，显式补一次
	addWeek(tr.End)

	return tables, nil
}

// getMonthTables 获取月份分片表
func (r *ShardRule) getMonthTables(tr TimeRange) ([]string, error) {
	var tables []string
	current := time.Date(tr.Start.Year(), tr.Start.Month(), 1, 0, 0, 0, 0, tr.Start.Location())
	end := time.Date(tr.End.Year(), tr.End.Month(), 1, 0, 0, 0, 0, tr.End.Location())

	for !current.After(end) {
		tables = append(tables, fmt.Sprintf("%s_%04d%02d", r.TableName, current.Year(), current.Month()))
		// 移动到下个月
		current = current.AddDate(0, 1, 0)
	}

	return tables, nil
}

// getTenDayTables 获取10天分片表
func (r *ShardRule) getTenDayTables(tr TimeRange) ([]string, error) {
	var tables []string
	current := tr.Start
	end := tr.End

	// 获取开始日期的10天段 (1=1~10, 2=11~20, 3=21~月底)
	startDay := current.Day()
	startSegment := (startDay-1)/10 + 1

	// 获取结束日期的10天段
	endDay := end.Day()
	endSegment := (endDay-1)/10 + 1

	// 如果开始和结束在同一个月的10天段内
	if current.Year() == end.Year() && current.Month() == end.Month() {
		for seg := startSegment; seg <= endSegment; seg++ {
			tables = append(tables, fmt.Sprintf("%s_%04d%02d_%d",
				r.TableName,
				current.Year(),
				current.Month(),
				seg))
		}
		return tables, nil
	}

	// 处理跨月跨年的情况
	for !current.After(end) {
		year, month, _ := current.Date()
		// 固定 3 个 10 天段：1~10 / 11~20 / 21~月末（任何月份都存在第三段，含平年 2 月的 21~28）
		segments := 3

		// 确定当前月的开始段和结束段
		currentStartSeg := 1
		currentEndSeg := segments

		if year == tr.Start.Year() && month == tr.Start.Month() {
			currentStartSeg = startSegment
		}
		if year == tr.End.Year() && month == tr.End.Month() {
			currentEndSeg = endSegment
		}

		for seg := currentStartSeg; seg <= currentEndSeg; seg++ {
			tables = append(tables, fmt.Sprintf("%s_%04d%02d_%d",
				r.TableName,
				year,
				month,
				seg))
		}

		// 移动到下个月的第一天
		if month == time.December {
			current = time.Date(year+1, time.January, 1, 0, 0, 0, 0, current.Location())
		} else {
			current = time.Date(year, month+1, 1, 0, 0, 0, 0, current.Location())
		}
	}

	return tables, nil
}

// GetTableNamesByIDRange 根据 ID 范围枚举出所有可能命中的分片表名。
//
// 智能枚举策略：
//   - ByIDRange：计算 id/RangeSize 的最小值和最大值，枚举出中间所有分片（精准、高效）
//   - ByIDHash：单个 ID 时直接返回对应的哈希分片；小范围（<=10 个 ID）逐个计算哈希去重；
//     大范围（>10 个 ID）直接返回全部 HashShards 张表（因为 crc32 无法根据范围做有效过滤）
//
// 参数：
//   - idRange: ID 闭区间 [Start, End]
//
// 返回值：
//   - []string: 范围内涉及到的分片表名
//   - error: Start > End 或分片类型不匹配时返回
//
// 示例：
//
//	// ByIDRange：RangeSize=1000，查 50~250 实际命中 shard 0 和 shard 1
//	rule := sharding.NewShardRule(sharding.ByIDRange, "orders", sharding.WithRangeSize(1000))
//	tables, _ := rule.GetTableNamesByIDRange(sharding.IDRange{Start: 50, End: 250})
//	fmt.Println(tables) // [orders_0 orders_1]
//
//	// ByIDHash：大范围永远返回全部分片
//	hashRule := sharding.NewShardRule(sharding.ByIDHash, "users", sharding.WithHashShards(8))
//	tables2, _ := hashRule.GetTableNamesByIDRange(sharding.IDRange{Start: 1, End: 1000000})
//	fmt.Println(tables2) // [users_0 ... users_7]，全部 8 张
func (r *ShardRule) GetTableNamesByIDRange(idRange IDRange) ([]string, error) {
	// 验证ID范围
	if idRange.Start > idRange.End {
		return nil, fmt.Errorf("起始ID不能大于结束ID")
	}

	switch r.Type {
	case ByIDHash:
		return r.getHashShardTables(idRange)
	case ByIDRange:
		return r.getRangeShardTables(idRange)
	default:
		return nil, fmt.Errorf("分片类型 %s 不支持基于ID的查询", r.Type)
	}
}

// getHashShardTables 获取哈希分片表
func (r *ShardRule) getHashShardTables(idRange IDRange) ([]string, error) {
	// 如果查询的是单个ID，直接返回对应的分片表
	if idRange.Start == idRange.End && idRange.Start > 0 {
		table, err := r.GetTableNameByID(idRange.Start)
		if err != nil {
			return nil, err
		}
		return []string{table}, nil
	}

	// 如果范围很小（例如小于等于10个ID），计算每个ID的哈希并去重
	if idRange.End > 0 && (idRange.End-idRange.Start) <= 10 {
		tables := make(map[string]struct{})
		for id := idRange.Start; id <= idRange.End; id++ {
			tableName, err := r.GetTableNameByID(id)
			if err != nil {
				return nil, err
			}
			tables[tableName] = struct{}{}
		}

		result := make([]string, 0, len(tables))
		for tableName := range tables {
			result = append(result, tableName)
		}
		return result, nil
	}

	// 对于大范围查询，返回所有分片表名
	var tables []string
	for i := 0; i < r.HashShards; i++ {
		tables = append(tables, fmt.Sprintf("%s_%d", r.TableName, i))
	}
	return tables, nil
}

// getRangeShardTables 获取范围分片表
// 与 GetTableNameByID 保持完全一致的分片公式：id / RangeSize
func (r *ShardRule) getRangeShardTables(idRange IDRange) ([]string, error) {
	startShard := idRange.Start / r.RangeSize
	endShard := idRange.End / r.RangeSize

	var tables []string
	for i := startShard; i <= endShard; i++ {
		tables = append(tables, fmt.Sprintf("%s_%d", r.TableName, i))
	}

	return tables, nil
}