// sharding 示例：分布式 ID 生成 + 分表路由 + 跨分片表名枚举。
// 纯逻辑演示，不需要任何数据库连接。
//
// 运行：go run ./examples/sharding
package main

import (
	"fmt"
	"log"
	"time"

	"github.com/yuwen002/meteor-forge/pkg/sharding"
)

func main() {
	// ── 1. 分布式 ID：生成与解析 ───────────────────────────────
	gen, err := sharding.NewSonyflakeIDGenerator(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		log.Fatalf("初始化 ID 生成器失败: %v", err)
	}
	id, err := gen.Generate()
	if err != nil {
		log.Fatalf("生成 ID 失败: %v", err)
	}
	info, _ := gen.Parse(id)
	fmt.Printf("生成的分布式 ID: %d（时间≈%s，机器ID=%d，序列=%d）\n",
		id, info.Timestamp.Format(time.RFC3339), info.MachineID, info.Sequence)

	// ── 2. 分表路由：按时间 / 按 ID 哈希 ──────────────────────
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.Local)
	byMonth := sharding.NewShardRule(sharding.ByMonth, "orders")
	monthTable, _ := byMonth.GetTableNameByTime(now)
	fmt.Printf("按月分片：当前写入表 = %s\n", monthTable)

	byHash := sharding.NewShardRule(sharding.ByIDHash, "users", sharding.WithHashShards(32))
	hashTable, _ := byHash.GetTableNameByID(int64(id % 1e6))
	fmt.Printf("按 ID 哈希分片：用户 %d 落在表 = %s\n", id, hashTable)

	// ── 3. 跨分片查询前的表枚举：命中时间范围内的全部分片表 ────
	start := time.Date(2026, 1, 15, 0, 0, 0, 0, time.Local)
	end := time.Date(2026, 4, 3, 0, 0, 0, 0, time.Local)
	tables, err := byMonth.GetTableNamesByTimeRange(sharding.TimeRange{Start: start, End: end})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("跨分片查询需扫描的表（%s ~ %s）: %v\n",
		start.Format("2006-01-02"), end.Format("2006-01-02"), tables)

	// ── 4. 与迁移引擎的配合思路 ──────────────────────────────
	// 真实场景中：
	//   - 用 sharding.NewMySQLTableManager(gormDB) + ShardTableManager 按模板建出上述分片表；
	//   - 用 Scheduler 按月提前自动建表；
	//   - 用 QueryBuilder.Tables(tables...).Where(...).Find(&dest) 做 UNION ALL / 并行跨片查询；
	//   - 各分片库的 schema 版本管理交给 meteor-forge 的 migration 引擎（tenants-up 并发逐库升级）。
	fmt.Println("\n提示：分片库的结构迁移请使用 meteor-forge 的 tenants-up / tenants-sync 命令")
}