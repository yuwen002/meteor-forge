package sharding

import (
	"context"
	"log"
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
// 参数:
//   - manager: 分片表管理器
//   - shardRule: 分片规则
//   - interval: 检查间隔时间
//   - monthsAhead: 提前创建的月数
func NewScheduler(manager *ShardTableManager, shardRule *ShardRule, interval time.Duration, monthsAhead int) *Scheduler {
	return &Scheduler{
		manager:     manager,
		shardRule:   shardRule,
		interval:    interval,
		monthsAhead: monthsAhead,
		stopChan:    make(chan struct{}),
	}
}

// Start 启动调度器
func (s *Scheduler) Start() {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()

		// 立即执行一次
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

// createShardTables 创建分片表
func (s *Scheduler) createShardTables() {
	ctx := context.Background()
	now := time.Now()
	endDate := now.AddDate(0, s.monthsAhead, 0)

	switch s.shardRule.Type {
	case ByMonth:
		for d := now; d.Before(endDate); d = d.AddDate(0, 1, 0) {
			shardName := d.Format("200601")
			tableName := s.shardRule.GetTableNameByTime(d)

			exists, err := s.manager.TableExists(ctx, shardName)
			if err != nil {
				log.Printf("检查表 %s 是否存在时出错: %v", tableName, err)
				continue
			}

			if !exists {
				if err := s.manager.CreateShardTable(ctx, shardName); err != nil {
					log.Printf("创建分片表 %s 失败: %v", tableName, err)
					continue
				}
				log.Printf("成功创建分片表: %s", tableName)
			}
		}
	// 可以添加其他分片类型的处理
	default:
		log.Printf("不支持的分片类型: %s", s.shardRule.Type)
	}
}

// GetNextShardTable 获取下一个分片表名
func (s *Scheduler) GetNextShardTable() string {
	// 获取下个月的表名
	nextMonth := time.Now().AddDate(0, 1, 0)
	return s.shardRule.GetTableNameByTime(nextMonth)
}

// GetCurrentShardTable 获取当前分片表名
func (s *Scheduler) GetCurrentShardTable() string {
	return s.shardRule.GetTableNameByTime(time.Now())
}

// ListAllShardTables 列出所有分片表
func (s *Scheduler) ListAllShardTables() ([]string, error) {
	return s.manager.ListShardTables(context.Background())
}