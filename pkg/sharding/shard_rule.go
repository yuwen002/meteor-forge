package sharding

import (
	"fmt"
	"hash/crc32"
	"strconv"
	"time"
)

// ShardType 定义分片策略类型
type ShardType string

// 定义不同的分片策略常量
const (
	ByYear    ShardType = "year"     // 按年分片
	ByQuarter ShardType = "quarter"  // 按季度分片
	ByMonth   ShardType = "month"    // 按月分片
	ByWeek    ShardType = "week"     // 按周分片
	ByTenDay  ShardType = "ten_day"  // 按10天分片
	ByIDHash  ShardType = "id_hash"  // 按ID哈希分片
	ByIDRange ShardType = "id_range" // 按ID范围分片
)

// ShardRule 定义分片规则
type ShardRule struct {
	Type       ShardType // 使用的分片策略
	TableName  string    // 基础表名，将作为分表的前缀
	HashShards int       // 哈希分表数量（用于 ByIDHash 策略）
	RangeSize  int64     // 每个分表存储的 ID 数量（用于 ByIDRange 策略）
}

// Option 定义可选参数类型，用于函数式选项模式
// 这种模式允许灵活地配置 ShardRule 实例
type Option func(*ShardRule)

// WithHashShards 设置哈希分片数量
// 参数:
//   - n: 要设置的哈希分片数量，必须大于0才会生效
//
// 返回值:
//   - Option: 返回一个配置函数，该函数会设置 ShardRule 的 HashShards 字段
//
// 使用示例:
//
//	rule := NewShardRule(ByIDHash, "users", WithHashShards(32))
func WithHashShards(n int) Option {
	return func(r *ShardRule) {
		if n > 0 {
			r.HashShards = n
		}
	}
}

// WithRangeSize 设置范围分片的大小
// 参数:
//   - size: 每个分片存储的ID数量，必须大于0才会生效
//
// 返回值:
//   - Option: 返回一个配置函数，该函数会设置 ShardRule 的 RangeSize 字段
//
// 使用示例:
//
//	rule := NewShardRule(ByIDRange, "orders", WithRangeSize(5000))
func WithRangeSize(size int64) Option {
	return func(r *ShardRule) {
		if size > 0 {
			r.RangeSize = size
		}
	}
}

// NewShardRule 创建并返回一个新的 ShardRule 实例
// 参数:
//   - shardType: 分片类型，如 ByYear, ByIDHash 等
//   - tableName: 基础表名
//   - opts: 可选的配置函数，用于设置分片参数
//
// 返回值:
//   - *ShardRule: 初始化后的分片规则实例
//
// 使用示例:
//
//	// 创建一个按月分片的规则
//	rule1 := NewShardRule(ByMonth, "logs")
//
//	// 创建一个带自定义参数的哈希分片规则
//	rule2 := NewShardRule(ByIDHash, "users", WithHashShards(64))
func NewShardRule(shardType ShardType, tableName string, opts ...Option) *ShardRule {
	r := &ShardRule{
		Type:       shardType,
		TableName:  tableName,
		HashShards: 16,      // 默认哈希分片数，适用于 ByIDHash 策略
		RangeSize:  1000000, // 默认范围分片大小，适用于 ByIDRange 策略
	}
	// 应用所有选项配置
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// TimeRange 表示一个时间范围，用于批量获取表名
type TimeRange struct {
	Start time.Time // 开始时间（包含）
	End   time.Time // 结束时间（包含）
}

// IDRange 表示一个ID范围，用于批量获取表名
type IDRange struct {
	Start int64 // 起始ID（包含）
	End   int64 // 结束ID（包含）
}

// GetTableNameByTime 根据“时间分片”规则返回实际的表名
// 适用于: ByYear / ByQuarter / ByMonth / ByWeek / ByTenDay
//
// 参数:
//   - t: 时间对象
//
// 返回值:
//   - string: 分片表名
//
// 示例:
//
//	rule := NewShardRule(ByMonth, "orders", 0, 0)
//	table := rule.GetTableNameByTime(time.Now())
//	// 结果可能是: orders_202508
func (r *ShardRule) GetTableNameByTime(t time.Time) string {
	switch r.Type {
	case ByYear:
		return fmt.Sprintf("%s_%d", r.TableName, t.Year())

	case ByQuarter:
		quarter := (int(t.Month())-1)/3 + 1
		return fmt.Sprintf("%s_%dQ%d", r.TableName, t.Year(), quarter)

	case ByMonth:
		return fmt.Sprintf("%s_%d%02d", r.TableName, t.Year(), t.Month())

	case ByWeek:
		// 年份与周数统一取自 ISOWeek，避免跨年边界时历年份与 ISO 周数错配（如 1 月初对应上年的高周号）
		weekYear, week := t.ISOWeek()
		return fmt.Sprintf("%s_%dW%02d", r.TableName, weekYear, week)

	case ByTenDay:
		// 将一个月分成 3 个分片，每个分片约 10 天
		day := t.Day()
		dayGroup := 3
		if day <= 10 {
			dayGroup = 1
		} else if day <= 20 {
			dayGroup = 2
		}
		return fmt.Sprintf("%s_%d%02d_%d", r.TableName, t.Year(), t.Month(), dayGroup)

	default:
		return r.TableName
	}
}

// GetTableNameByID 根据“ID 分片”规则返回实际的表名
// 适用于: ByIDHash / ByIDRange
//
// 参数:
//   - auto_id: 业务数据的唯一 ID
//
// 返回值:
//   - string: 分片表名
//
// 示例:
//
//	rule := NewShardRule(ByIDHash, "users", 32, 0)
//	table := rule.GetTableNameByID(123456789)
//	// 结果可能是: users_5
func (r *ShardRule) GetTableNameByID(id int64) string {
	switch r.Type {
	case ByIDHash:
		// 如果未设置有效的分片数量，默认使用 16 个
		if r.HashShards <= 0 {
			r.HashShards = 16
		}
		shard := crc32.ChecksumIEEE([]byte(strconv.FormatInt(id, 10))) % uint32(r.HashShards)
		return fmt.Sprintf("%s_%d", r.TableName, shard)

	case ByIDRange:
		// 如果未设置范围大小，默认每个分片存储 100 万条数据
		if r.RangeSize <= 0 {
			r.RangeSize = 1000000
		}
		shard := id / r.RangeSize
		return fmt.Sprintf("%s_%d", r.TableName, shard)

	default:
		return r.TableName
	}
}

// GetTableNamesByTimeRange 根据时间范围获取所有相关的表名
// 参数:
//   - tr: 时间范围
//
// 返回值:
//   - []string: 包含时间范围内所有可能的分片表名
//   - error: 如果分片类型不是时间类型或时间范围无效
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

// GetTableNamesByIDRange 根据ID范围获取所有相关的表名
// 参数:
//   - idRange: ID范围
//
// 返回值:
//   - []string: 包含ID范围内所有可能的分片表名
//   - error: 如果分片类型不是ID类型或ID范围无效
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
		return []string{r.GetTableNameByID(idRange.Start)}, nil
	}

	// 如果范围很小（例如小于等于10个ID），计算每个ID的哈希并去重
	if idRange.End > 0 && (idRange.End-idRange.Start) <= 10 {
		tables := make(map[string]struct{})
		for id := idRange.Start; id <= idRange.End; id++ {
			tableName := r.GetTableNameByID(id)
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
func (r *ShardRule) getRangeShardTables(idRange IDRange) ([]string, error) {
	var tables []string

	// 计算起始和结束的分片索引
	startShard := (idRange.Start - 1) / r.RangeSize
	endShard := (idRange.End - 1) / r.RangeSize

	// 遍历所有相关的分片
	for i := startShard; i <= endShard; i++ {
		tables = append(tables, fmt.Sprintf("%s_%d", r.TableName, i))
	}

	return tables, nil
}
