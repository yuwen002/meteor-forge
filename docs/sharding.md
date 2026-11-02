# pkg/sharding — 分库分表运行时路由与查询

本包由作者的 GoShard 项目合并而来，与 `pkg/migration` 的 Schema 版本管理互补：

```
迁移引擎 (pkg/migration)            sharding 包 (pkg/sharding)
    负责 schema 演进                      负责运行时路由
    tenants-up / tenants-sync              数据往哪写、跨片怎么查
    逐库逐片升级 DDL                       分片表自动创建
```

> **依赖边界**：本包是全仓唯一引入 `gorm.io/gorm`（表管理/查询执行）与 `github.com/sony/sonyflake` 的模块。`pkg/migration` 与 `pkg/driver` 保持零 ORM 依赖不变。
>
> 本包不引入任何 gorm 方言器（dialector），`*gorm.DB` 由调用方注入。若与迁移引擎共用同一数据库，建议 `gorm.Open(mysql.New(mysql.Config{Conn: sqlDB}), ...)` 复用已有 `*sql.DB`，避免对同一个实例开两套连接池。

---

## 目录

1. [SonyflakeIDGenerator — 分布式 ID](#1-sonyflakeidgenerator--分布式-id)
2. [ShardRule — 分片规则与路由](#2-shardrule--分片规则与路由)
3. [TableManager / ShardTableManager — 分片表管理](#3-tablemanager--shardtablemanager--分片表管理)
4. [Scheduler — 定时提前建表](#4-scheduler--定时提前建表)
5. [QueryBuilder — 跨分片查询](#5-querybuilder--跨分片查询)
6. [各数据库实现对比](#6-各数据库实现对比)
7. [安全设计](#7-安全设计)
8. [完整示例](#8-完整示例)

---

## 1. SonyflakeIDGenerator — 分布式 ID

基于 Sonyflake 改造的全局唯一 ID 生成器，位段布局：

```
| 1 bit (保留) | 39 bit 时间戳 (起始时间以来的毫秒数) | 16 bit 机器 ID | 8 bit 序列号 |
```

### API

```go
gen, err := sharding.NewSonyflakeIDGenerator(startTime)   // 创建，startTime 自定义起始时间
id, err := gen.Generate()                                  // 生成一个 ID
info, err := gen.Parse(id)                                 // 反解出时间戳/机器ID/序列号

// 包级便捷函数（全局单例，InitSonyflake 必须在 GenerateID/ParseID 之前调用）
sharding.InitSonyflake(startTime)                          // 初始化全局实例
id, _ := sharding.GenerateID()                             // 包级生成
info, _ := sharding.ParseID(id)                            // 包级解析

// 历史数据回填：根据时间戳和序列号生成确定性 ID（同一输入必然同一输出）
id, _ := sharding.GenerateIDFromTimestamp(someTime, seq)
```

`IDInfo` 结构：

```go
type IDInfo struct {
    Timestamp time.Time  // 反解出的生成时间
    MachineID uint16     // 机器 ID
    Sequence  uint16     // 序列号
    Original  uint64     // 原始 ID
}
```

### 机器 ID 自动发现

Sonyflake 默认从机器 IP 最后一段推导 machineID。容器/K8s 环境下可通过环境变量 `SONYFLAKE_MACHINE_ID` 覆盖。

---

## 2. ShardRule — 分片规则与路由

### 7 种分片策略

| 常量 | 值 | 分片依据 | 典型场景 |
|------|-----|---------|---------|
| `ByYear` | `"year"` | 时间按年分段 | 归档历史数据 |
| `ByQuarter` | `"quarter"` | 时间按季度分段 | 报表类数据 |
| `ByMonth` | `"month"` | 时间按月分段 | 订单/交易（最常用） |
| `ByWeek` | `"week"` | 时间按 ISO 周分段 | 短周期高频数据 |
| `ByTenDay` | `"ten_day"` | 每月分 3 段（1-10/11-20/21-end） | 月末月初热度不均的场景 |
| `ByIDHash` | `"id_hash"` | `hash(ID) % N` | 无时间维度、均匀分布的写多型 |
| `ByIDRange` | `"id_range"` | `ID / rangeSize` | 按业务 ID 段预分配 |

### 构造

```go
// 时间分片（不需要 Option）
rule := sharding.NewShardRule(sharding.ByMonth, "orders")

// ID 哈希分片：指定哈希分片数量
rule := sharding.NewShardRule(sharding.ByIDHash, "events",
    sharding.WithHashShards(16),
)

// ID 范围分片：指定每段 ID 范围大小
rule := sharding.NewShardRule(sharding.ByIDRange, "logs",
    sharding.WithRangeSize(1000000),
)
```

### 单点路由 — 查表名

```go
// 时间分片：给定时间，返回表名
table := rule.GetTableNameByTime(time.Now())   // "orders_202611"

// ID 分片：给定 ID，返回表名
table := rule.GetTableNameByID(12345)          // "events_7"（hash(12345)%16）
```

### 范围枚举 — 跨片查所有表

```go
// 时间范围：返回 [start, end] 覆盖的所有分片表名
tables, _ := rule.GetTableNamesByTimeRange(sharding.TimeRange{
    Start: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
    End:   time.Date(2026, 11, 30, 23, 59, 59, 0, time.UTC),
})
// → ["orders_202609", "orders_202610", "orders_202611"]

// ID 范围：同理
tables, _ := rule.GetTableNamesByIDRange(sharding.IDRange{Start: 0, End: 500000})
```

> **一致性保证**：`GetTableNamesByTimeRange` 枚举的表名与 `GetTableNameByTime` 单点查表名严格一致，跨片查询不会漏表或查错表。

### 各策略表名格式

| 分片类型 | 示例表名 | 格式规则 |
|---------|---------|---------|
| `ByYear` | `orders_2026` | `baseName + "_" + YYYY` |
| `ByQuarter` | `orders_2026Q4` | `baseName + "_" + YYYY + "Q" + 1~4` |
| `ByMonth` | `orders_202611` | `baseName + "_" + YYYYMM` |
| `ByWeek` | `orders_2026W44` | `baseName + "_" + ISO周编号` |
| `ByTenDay` | `orders_202611_2` | `baseName + "_" + YYYYMM + "_" + 段号(1/2/3)` |
| `ByIDHash` | `events_7` | `baseName + "_" + hash(ID)%N` |
| `ByIDRange` | `logs_0` | `baseName + "_" + ID/rangeSize` |

注：`ByWeek` 使用 `time.Time.ISOWeek()` 的 ISO 年而非公历年，跨年时可能与直觉略有差异（例如 2026-01-01 可能属于 2025-W53），这是有意的设计以保证周编号连续。

---

## 3. TableManager / ShardTableManager — 分片表管理

### TableManager 接口 — 数据库无关的表操作抽象

```go
type TableManager interface {
    TableExists(ctx context.Context, table string) (bool, error)
    CreateTable(ctx context.Context, table, sql string) error
    CreateTableLike(ctx context.Context, newTable, sourceTable string) error
    DropTable(ctx context.Context, table string) error
    ListTables(ctx context.Context, prefix string) ([]string, error)
}
```

三个数据库实现见 [第 6 节](#6-各数据库实现对比)。

### ShardTableManager — 高层封装

把 `TableManager` + `ShardRule` 粘合起来，用 **分片名**（shardName，如 `"202611"`、`"7"`）操作，而非完整表名：

```go
mgr := sharding.NewShardTableManager(
    tableManager,      // TableManager 实现
    rule,              // ShardRule
    createTableSQL,    // CREATE TABLE 模板（允许完整语句或仅表定义）
    nil,               // 可选：表名转换函数，nil 则用 shardName 直接作表名后缀
)

exists, _ := mgr.TableExists(ctx, "202611")
mgr.CreateShardTable(ctx, "202611")
mgr.CreateShardTableLike(ctx, "202612", "202611")
tables, _ := mgr.ListShardTables(ctx)
```

模板管理：

```go
mgr.LoadTemplateFromFile("./migrations/orders_template.sql")
mgr.LoadTemplateFromString(`CREATE TABLE orders (...) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`)
tpl := mgr.GetCurrentTemplate()
```

### CreateTable 模板格式

支持两种输入，各数据库实现会自动处理：

- **完整 CREATE TABLE 语句**：自动替换表名 + 加 `IF NOT EXISTS`
- **仅表定义**：自动补 `CREATE TABLE IF NOT EXISTS {tableName}` 前缀

---

## 4. Scheduler — 定时提前建表

后台协程定期检查并创建未来缺失的分片表。适合提前按月/按年建好下一个周期的表，避免业务高峰时因首次建表阻塞写入。

### 构造与启动

```go
sched := sharding.NewScheduler(
    shardTableMgr,     // *ShardTableManager
    rule,              // *ShardRule
    1*time.Hour,       // 检查间隔
    6,                 // monthsAhead：提前多少个月（适用于所有时间分片类型的换算）
)

sched.Start()         // 立即执行一次 + 按 interval 周期执行
defer sched.Stop()    // 优雅退出
```

### 提前建表策略

| 分片类型 | 步进周期 | 提前策略 | shardName 格式 |
|---------|---------|---------|---------------|
| `ByYear` | 1 年 | `ceil(monthsAhead/12) + 2` 年 | `"2026"` |
| `ByQuarter` | 3 个月 | `ceil(monthsAhead/3) + 2` 季度 | `"2026Q4"` |
| `ByMonth` | 1 个月 | `monthsAhead + 2` 月 | `"202611"` |
| `ByWeek` | 7 天 | `monthsAhead×5 + 2` 周 | `"2026W44"` |
| `ByTenDay` | 10 天 | `monthsAhead×3 + 2` 段 | `"202611_2"` |
| `ByIDHash` | — | 枚举全部 `HashShards` 个分片（固定数量） | `"0"` ~ `"15"` |
| `ByIDRange` | — | 不支持定时建表（分片取决于运行时业务 ID，无法预建） | — |

> 每种类型都额外 +2 的安全余量，避免边界时刻（如月末最后一天）因计算误差漏建。

### 辅助查询

```go
sched.GetCurrentShardTable()   // 当前周期的表名，如 "orders_202611"（ID 分片返回空串）
sched.GetNextShardTable()      // 下一个周期的表名
sched.ListAllShardTables()     // 列出数据库中实际已存在的所有分片表
```

---

## 5. QueryBuilder — 跨分片查询

基于 gorm 的链式查询构建器，支持两种跨片查询模式：

### 模式 A：UNION ALL 合并查询（适合低分片数、小结果集）

```go
var dest []Order
err := sharding.NewQueryBuilder(gormDB, rule).
    Tables("orders_202609", "orders_202610", "orders_202611").
    Where("user_id = ?", 42).
    Order("amount DESC").
    Limit(20).Offset(0).
    Find(&dest)
```

内部会生成：

```sql
(SELECT * FROM orders_202609 WHERE user_id = 42)
UNION ALL
(SELECT * FROM orders_202610 WHERE user_id = 42)
UNION ALL
(SELECT * FROM orders_202611 WHERE user_id = 42)
ORDER BY amount DESC LIMIT 20 OFFSET 0
```

### 模式 B：并行查询 + 应用层归并分页（适合高分片数、大数据集）

```go
err := sharding.NewQueryBuilder(gormDB, rule).
    Tables(tables...).
    Where("status = ?", 4).
    Order("created_at DESC").
    Limit(100).Offset(200).
    FindParallel(&dest, 8)   // 8 = 最大并发 goroutine 数
```

每个分片独立执行 `SELECT ... ORDER BY`，结果在应用层做 **多路归并** 后再应用全局 `LIMIT/OFFSET`。这保证了跨片排序 + 分页的正确性——直接 UNION ALL + LIMIT 无法做到全局有序分页。

### 链式 API 总览

```go
.NewQueryBuilder(db, rule).
    Tables(tables...).              // 必填：要查的分片表列表（白名单校验）
    Select(columns...).             // 可选：SELECT 列
    Where(query, args...).          // 可选：AND 条件，可多次调用累加
    OrWhere(query, args...).        // 可选：OR 条件
    Group(columns...).              // 可选
    Having(having, args...).        // 可选
    Order(order).                   // 可选
    Limit(n).Offset(m).             // 可选
    Find(&dest)                     // UNION ALL 模式
    FindParallel(&dest, concurrency) // 并行 + 归并模式
    First(&dest)                    // 取第一条（UNION ALL 模式）
```

> 两种模式都要求显式调用 `.Tables(...)`，QueryBuilder 不会自动枚举分片——调用方决定查哪些表（常见用法是先 `rule.GetTableNamesByTimeRange(...)` 得到列表再传入）。

---

## 6. 各数据库实现对比

三个实现共享同一个 `TableManager` 接口，差异点在各数据库的语法特性：

### MySQLTableManager

| 方法 | MySQL 语法 |
|------|-----------|
| `TableExists` | `SELECT 1 FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ? LIMIT 1` |
| `CreateTable` | 支持完整 CREATE TABLE 或仅表定义；自动替换表名 + 加 `IF NOT EXISTS` |
| `CreateTableLike` | `CREATE TABLE IF NOT EXISTS new LIKE source` |
| `DropTable` | `DROP TABLE IF EXISTS \`name\`` |
| `ListTables` | `information_schema.tables` + `REGEXP` 精确匹配 |

### PostgresTableManager

| 方法 | PostgreSQL 语法 |
|------|----------------|
| `TableExists` | `information_schema.tables` + `current_schema()` |
| `CreateTable` | 完整 CREATE TABLE 语句 |
| `CreateTableLike` | 主方案：`CREATE TABLE ... (LIKE ... INCLUDING ALL)`；降级：`CREATE TABLE ... AS ... WITH NO DATA` |
| `DropTable` | `DROP TABLE IF EXISTS "name"`（双引号转义表名） |
| `ListTables` | `information_schema.tables` + `table_name LIKE $1` |

### ClickHouseTableManager

| 方法 | ClickHouse 语法 |
|------|----------------|
| `TableExists` | `SELECT count() FROM system.tables WHERE database = currentDatabase() AND name = ?` |
| `CreateTable` | **SQL 模板必须显式包含 ENGINE 与 ORDER BY**（ClickHouse 强制要求） |
| `CreateTableLike` | `CREATE TABLE IF NOT EXISTS new AS source`（ClickHouse AS 只复制结构不带数据） |
| `DropTable` | `DROP TABLE IF EXISTS \`name\`` |
| `ListTables` | `system.tables` + `name LIKE ?` |

### 选择哪个

```go
// MySQL
tableMgr := sharding.NewMySQLTableManager(gorm.Open(mysql.Open(dsn), &gorm.Config{}))

// PostgreSQL
tableMgr := sharding.NewPostgresTableManager(gorm.Open(postgres.Open(dsn), &gorm.Config{}))

// ClickHouse（需要 clickhouse-go/v2 驱动）
tableMgr := sharding.NewClickHouseTableManager(gorm.Open(clickhouse.Open(dsn), &gorm.Config{}))
```

---

## 7. 安全设计

### 表名白名单

所有动态拼接的表名均经 `isValidTableName` 校验，只允许：

```regexp
^[a-zA-Z_][a-zA-Z0-9_]*$
```

白名单校验在两个位置执行：
- `TableManager.CreateTable` / `DropTable` / `CreateTableLike`：防止调用方传入恶意表名
- `QueryBuilder.Tables`：防止跨片查询时表名注入

### ClickHouse 建表模板提醒

ClickHouse 不支持无 ENGINE 的建表，调用方必须提供完整模板（含 `ENGINE = MergeTree()` 和 `ORDER BY`）。

---

## 8. 完整示例

### 时间分片（按月）+ MySQL + Scheduler

```go
import (
    "time"
    "gorm.io/gorm"
    "gorm.io/driver/mysql"
    "github.com/yuwen002/meteor-forge/pkg/sharding"
)

func main() {
    // 1. 准备 *gorm.DB（建议复用迁移引擎的 *sql.DB）
    db, _ := gorm.Open(mysql.Open(dsn), &gorm.Config{})

    // 2. 初始化分布式 ID
    sharding.InitSonyflake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

    // 3. 定义分片规则
    rule := sharding.NewShardRule(sharding.ByMonth, "orders")

    // 4. 初始化分片表管理器 + 建表模板
    tableMgr := sharding.NewMySQLTableManager(db)
    shardMgr := sharding.NewShardTableManager(tableMgr, rule, "", nil)
    shardMgr.LoadTemplateFromString(`
        CREATE TABLE orders (
            id          BIGINT PRIMARY KEY,
            user_id     BIGINT NOT NULL,
            amount      DECIMAL(10,2) NOT NULL,
            status      TINYINT DEFAULT 0,
            created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
            INDEX idx_user_status (user_id, status)
        ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
    `)

    // 5. 启动定时建表（可选）
    sched := sharding.NewScheduler(shardMgr, rule, 1*time.Hour, 6)
    sched.Start()
    defer sched.Stop()

    // 6. 写入：生成 ID + 路由到目标分片
    id, _ := sharding.GenerateID()
    writeTable := rule.GetTableNameByTime(time.Now())
    db.Table(writeTable).Create(&Order{ID: id, ...})

    // 7. 跨片查询：枚举时间范围 -> QueryBuilder
    tables, _ := rule.GetTableNamesByTimeRange(sharding.TimeRange{
        Start: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
        End:   time.Date(2026, 11, 30, 23, 59, 59, 0, time.UTC),
    })
    var dest []Order
    sharding.NewQueryBuilder(db, rule).
        Tables(tables...).
        Where("user_id = ? AND status = ?", 42, 4).
        Order("created_at DESC").
        Limit(20).Offset(0).
        Find(&dest)
}
```

### ID 哈希分片 + Postgres

```go
rule := sharding.NewShardRule(sharding.ByIDHash, "events",
    sharding.WithHashShards(16),
)
tableMgr := sharding.NewPostgresTableManager(gormDB)

table := rule.GetTableNameByID(eventID)
db.Table(table).Create(&Event{ID: eventID, ...})
```

### ClickHouse 注意事项

```go
tableMgr := sharding.NewClickHouseTableManager(gormDB)
shardMgr := sharding.NewShardTableManager(tableMgr, rule, "", nil)

// ClickHouse 模板必须含 ENGINE + ORDER BY
shardMgr.LoadTemplateFromString(`
    CREATE TABLE events_template (
        id UInt64,
        user_id UInt64,
        ts DateTime
    ) ENGINE = MergeTree()
      ORDER BY (user_id, ts);
`)
```

---

## 文件清单

| 文件 | 职责 |
|------|------|
| `table_manager.go` | TableManager 接口 + isValidTableName + cleanSQLTemplate |
| `mysql_table_manager.go` | MySQL 实现 |
| `postgres_table_manager.go` | PostgreSQL 实现 |
| `clickhouse_table_manager.go` | ClickHouse 实现 |
| `sonyflake_id.go` | Sonyflake 分布式 ID 生成/解析 |
| `shard_rule.go` | ShardRule + 7 种 ShardType + 路由/枚举 |
| `shard_table_manager.go` | ShardTableManager 高层封装 |
| `scheduler.go` | 全分片类型定时提前建表后台协程 |
| `query_builder.go` | UNION ALL + 并行归并跨片查询 |
| `doc.go` | 包级注释（边界/依赖/安全设计） |