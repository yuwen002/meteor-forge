// Package sharding 提供分库分表场景下的运行时路由与查询能力（源自 GoShard 项目合并），
// 与 pkg/migration 的 Schema 版本管理互补：分片库/分片表的 schema 演进交给迁移引擎
// （tenants-up / tenants-sync 逐库逐片升级），本包负责"数据往哪写、跨片怎么查"。
//
// # 模块总览
//
//   - SonyflakeIDGenerator：全局唯一分布式 ID（39 位时间 | 16 位机器 | 8 位序列），
//     支持 Parse 反解与 GenerateFromTimestamp 历史数据回填；
//   - ShardRule：7 种分片策略（ByYear/ByQuarter/ByMonth/ByWeek/ByTenDay/ByIDHash/ByIDRange）
//     的表名路由与时间范围跨片表枚举（枚举与单点路由命名严格一致）；
//   - TableManager / ShardTableManager：按模板自动建分片表（CREATE TABLE LIKE 克隆结构），
//     已提供 MySQLTableManager / PostgresTableManager / ClickHouseTableManager 三种实现；
//     Scheduler 支持全部时间分片类型与 ID 哈希分片的定时提前建表；
//   - QueryBuilder：跨分片 UNION ALL 合并查询与并行分片查询 + 应用层归并分页。
//   - RawBuilder：原始 SQL + {table} 占位符的分片路由执行器，支持复杂 SQL 场景。
//
// # 依赖边界
//
// 本包是全仓唯一引入 gorm.io/gorm 与 github.com/sony/sonyflake 的模块，
// pkg/migration 与 pkg/driver 保持零 ORM 依赖不变。本包不引入任何 gorm 方言器（dialector）：
// *gorm.DB 由调用方注入，不主动连库；若与迁移引擎共用同一数据库，建议通过
// mysql.New(mysql.Config{Conn: sqlDB}) 复用已有 *sql.DB，避免对同一实例开两套连接池。
//
// # 安全
//
// QueryBuilder / TableManager 的动态表名均经白名单校验（仅字母/数字/下划线，
// 支持反引号与别名）后才进入 SQL 拼接，防止表名注入。
//
// # 快速开始示例
//
// 完整使用流程参考以下示例：
//
//	import (
//		"context"
//		"time"
//
//		"gorm.io/gorm"
//		"meteor-forge/pkg/sharding"
//	)
//
//	type Order struct {
//		ID        int64     `gorm:"column:id"`
//		UserID    int64     `gorm:"column:user_id"`
//		Amount    float64   `gorm:"column:amount"`
//		Status    int32     `gorm:"column:status"`
//		CreatedAt time.Time `gorm:"column:created_at"`
//	}
//
//	func main() {
//		// 1. 初始化 GORM DB（调用方负责连接）
//		db, _ := gorm.Open(mysql.Open("..."), &gorm.Config{})
//
//		// 2. 创建分片规则（按月分片）
//		rule := sharding.NewShardRule(sharding.ByMonth, "orders")
//
//		// 3. 使用 QueryBuilder 写入（自动路由到正确的分片表）
//		qb := sharding.NewQueryBuilder(db, rule)
//		orders := []Order{{UserID: 1, Amount: 99.9, Status: 1, CreatedAt: time.Now()}}
//		qb.CreateInBatches(&orders, 100)
//
//		// 4. 跨分片查询（自动枚举 2026-10 ~ 2026-11 的所有分片表）
//		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
//		defer cancel()
//
//		var results []Order
//		qb.WithContext(ctx).
//			TimeRange(sharding.TimeRange{
//				Start: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
//				End:   time.Date(2026, 11, 30, 23, 59, 59, 0, time.UTC),
//			}).
//			Where("status = ?", 1).
//			Order("created_at DESC").
//			Limit(20).Offset(40).
//			FindParallel(&results, 8)
//	}
package sharding