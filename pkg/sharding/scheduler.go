package sharding

import (
	"context"
	"log"
	"math"
	"sync"
	"time"
)

// Scheduler 管理分片表的定时创建任务
type Scheduler struct {
	manager     *ShardTableManager
	shardRule   *ShardRule
	interval    time.Duration
	monthsAhead int
	stopChan    chan struct{}
	wg          sync.WaitGroup
}

// NewScheduler 创建一个新的分片表调度器
// monthsAhead 表示提前多少个月（适用于所有时间分片类型的换算）
func NewScheduler(manager *ShardTableManager, shardRule *ShardRule, interval time.Duration, monthsAhead int) *Scheduler {
	return &Scheduler{
		manager:     manager,
		shardRule:   shardRule,
		interval:    interval,
		monthsAhead: monthsAhead,
		stopChan:    make(chan struct{}),
	}
}

// Start 启动调度器（立即执行一次 + 按 interval 周期执行）
func (s *Scheduler) Start() {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()

		s.createShardTables()

		for {
			select {
			case <-ticker.C:
				s.createShardTables()
			case <-s.stopChan:
				log.Println("分片表调度器已停止")
				return
			}
		}
	}()
}

// Stop 停止调度器
func (s *Scheduler) Stop() {
	close(s.stopChan)
	s.wg.Wait()
}

// createShardTables 根据分片类型提前创建未来缺失的分片表
func (s *Scheduler) createShardTables() {
	ctx := context.Background()
	rule := s.shardRule

	switch rule.Type {
	case ByYear:
		s.createYearTables(ctx)
	case ByQuarter:
		s.createQuarterTables(ctx)
	case ByMonth:
		s.createMonthTables(ctx)
	case ByWeek:
		s.createWeekTables(ctx)
	case ByTenDay:
		s.createTenDayTables(ctx)
	case ByIDHash:
		s.createIDHashTables(ctx)
	case ByIDRange:
		log.Printf("分片类型 ByIDRange 不支持定时提前建表（分片取决于运行时业务 ID）")
	default:
		log.Printf("不支持的分片类型: %s", rule.Type)
	}
}

// ensureShardTable 检查并创建单个分片表
func (s *Scheduler) ensureShardTable(ctx context.Context, shardName string) {
	exists, err := s.manager.TableExists(ctx, shardName)
	if err != nil {
		log.Printf("检查表 %s 是否存在时出错: %v", shardName, err)
		return
	}
	if exists {
		return
	}
	if err := s.manager.CreateShardTable(ctx, shardName); err != nil {
		log.Printf("创建分片表 %s 失败: %v", shardName, err)
		return
	}
	log.Printf("成功创建分片表: %s", shardName)
}

// createYearTables 按年分片：提前 ceil(monthsAhead/12) + 2 年
func (s *Scheduler) createYearTables(ctx context.Context) {
	extraYears := int(math.Ceil(float64(s.monthsAhead)/12)) + 2
	now := time.Now()
	for i := 0; i < extraYears; i++ {
		d := time.Date(now.Year()+i, 1, 1, 0, 0, 0, 0, now.Location())
		tableName, err := s.shardRule.GetTableNameByTime(d)
		if err != nil {
			continue
		}
		s.ensureShardTable(ctx, tableName)
	}
}

// createQuarterTables 按季度分片：提前 ceil(monthsAhead/3) + 2 季度
func (s *Scheduler) createQuarterTables(ctx context.Context) {
	extraQuarters := int(math.Ceil(float64(s.monthsAhead)/3)) + 2
	now := time.Now()
	for i := 0; i < extraQuarters; i++ {
		d := now.AddDate(0, i*3, 0)
		tableName, err := s.shardRule.GetTableNameByTime(d)
		if err != nil {
			continue
		}
		s.ensureShardTable(ctx, tableName)
	}
}

// createMonthTables 按月分片：提前 monthsAhead + 2 月
func (s *Scheduler) createMonthTables(ctx context.Context) {
	extraMonths := s.monthsAhead + 2
	now := time.Now()
	for i := 0; i < extraMonths; i++ {
		d := now.AddDate(0, i, 0)
		tableName, err := s.shardRule.GetTableNameByTime(d)
		if err != nil {
			continue
		}
		s.ensureShardTable(ctx, tableName)
	}
}

// createWeekTables 按周分片：提前 monthsAhead*5 + 2 周
func (s *Scheduler) createWeekTables(ctx context.Context) {
	extraWeeks := s.monthsAhead*5 + 2
	now := time.Now()
	for i := 0; i < extraWeeks; i++ {
		d := now.AddDate(0, 0, i*7)
		tableName, err := s.shardRule.GetTableNameByTime(d)
		if err != nil {
			continue
		}
		s.ensureShardTable(ctx, tableName)
	}
}

// createTenDayTables 按旬分片：提前 monthsAhead*3 + 2 段
func (s *Scheduler) createTenDayTables(ctx context.Context) {
	extraSegs := s.monthsAhead*3 + 2
	now := time.Now()

	distinct := make(map[string]struct{})
	count := 0
	for i := 0; count < extraSegs; i++ {
		d := now.AddDate(0, 0, i*10)
		tableName, err := s.shardRule.GetTableNameByTime(d)
		if err != nil {
			continue
		}
		if _, seen := distinct[tableName]; !seen {
			distinct[tableName] = struct{}{}
			count++
			s.ensureShardTable(ctx, tableName)
		}
	}
}

// createIDHashTables ID 哈希分片：枚举全部 HashShards 个分片
func (s *Scheduler) createIDHashTables(ctx context.Context) {
	for i := int64(0); i < int64(s.shardRule.HashShards); i++ {
		tableName, err := s.shardRule.GetTableNameByID(i)
		if err != nil {
			continue
		}
		s.ensureShardTable(ctx, tableName)
	}
}

// GetNextShardTable 获取下一个分片表名（非 ID 分片时有效）
func (s *Scheduler) GetNextShardTable() (string, error) {
	if s.shardRule.Type == ByIDHash || s.shardRule.Type == ByIDRange {
		return "", nil
	}
	switch s.shardRule.Type {
	case ByYear:
		return s.shardRule.GetTableNameByTime(time.Now().AddDate(1, 0, 0))
	case ByQuarter:
		return s.shardRule.GetTableNameByTime(time.Now().AddDate(0, 3, 0))
	case ByMonth:
		return s.shardRule.GetTableNameByTime(time.Now().AddDate(0, 1, 0))
	case ByWeek:
		return s.shardRule.GetTableNameByTime(time.Now().AddDate(0, 0, 7))
	case ByTenDay:
		return s.shardRule.GetTableNameByTime(time.Now().AddDate(0, 0, 10))
	default:
		return "", nil
	}
}

// GetCurrentShardTable 获取当前分片表名（非 ID 分片时有效）
func (s *Scheduler) GetCurrentShardTable() (string, error) {
	if s.shardRule.Type == ByIDHash || s.shardRule.Type == ByIDRange {
		return "", nil
	}
	return s.shardRule.GetTableNameByTime(time.Now())
}

// ListAllShardTables 列出数据库中实际已存在的所有分片表
func (s *Scheduler) ListAllShardTables() ([]string, error) {
	return s.manager.ListShardTables(context.Background())
}