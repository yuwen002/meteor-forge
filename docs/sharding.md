# pkg/sharding 完整接口文档

> 版本：v1.0 · 最后更新：2026-11
>
> 本包提供分库分表场景下的**运行时路由与查询能力**，源自 GoShard 项目合并。与 `pkg/migration` 的 Schema 版本管理互补：
>
> ```
> 迁移引擎 (pkg/migration)              sharding 包 (pkg/sharding)
>     负责 schema 演进                      负责运行时路由
>     tenants-up / tenants-sync              数据往哪写、跨片怎么查
>     逐库逐片升级 DDL                       分片表自动创建
> ```
>
> **依赖边界**：本包是全仓唯一引入 `gorm.io/gorm`（表管理/查询执行）与 `github.com/sony/sonyflake` 的模块。`pkg/migration` 与 `pkg/driver` 保持零 ORM 依赖不变。
>
> 本包不引入任何 gorm 方言器（dialector），`*gorm.DB` 由调用方注入。若与迁移引擎共用同一数据库，建议通过 `mysql.New(mysql.Config{Conn: sqlDB})` 复用已有 `*sql.DB`，避免对同一个实例开两套连接池。

---

## 目录

1. [模块总览与架构](#1-模块总览与架构)
2. [SonyflakeIDGenerator — 分布式 ID](#2-sonyflakeidgenerator--分布式-id)
3. [ShardRule — 分片规则与路由](#3-shardrule--分片规则与路由)
4. [TableManager 接口 — 数据库无关的表操作抽象](#4-tablemanager-接口--数据库无关的表操作抽象)
5. [ShardTableManager — 分片表管理 Facade](#5-shardtablemanager--分片表管理-facade)
6. [Scheduler — 定时提前建表](#6-scheduler--定时提前建表)
7. [QueryBuilder — 跨分片查询](#7-querybuilder--跨分片查询)
8. [RawBuilder — 原始 SQL 分片路由](#8-rawbuilder--原始-sql-分片路由)
9. [各数据库实现对比](#9-各数据库实现对比)
10. [安全设计](#10-安全设计)
11. [完整端到端示例](#11-完整端到端示例)
12. [公开 API 速查表](#12-公开-api-速查表)
13. [文件清单](#13-文件清单)

---

## 1. 模块总览与架构

### 核心组件关系图

```
┌─────────────────────┐
│  SonyflakeIDGenerator │  ← 分布式 ID 生成（39位时间|16位机器|8位序列）
└──────────┬──────────┘
           │ 生成的 ID 内含时间戳
           ▼
┌─────────────────────┐
│      ShardRule       │  ← 分片路由核心（7 种策略）
│  ├─ GetTableNameByTime    │     单点路由 → 写入
│  ├─ GetTableNameByID      │     单点路由 → 写入
│  ├─ GetTableNamesByTimeRange │  范围枚举 → 跨片查询
│  └─ GetTableNamesByIDRange   │  范围枚举 → 跨片查询
└──────────┬──────────┘
           │
     ┌─────┴──────┐
     ▼            ▼
┌──────────┐  ┌──────────────────┐
│ Scheduler │  │  QueryBuilder     │
│ 定时建表   │  │  ├─ Find (UNION ALL)      │
│           │  │  ├─ FindParallel (并行)   │
└────┬──────┘  │  ├─ Aggregate (聚合)      │
     │         │  ├─ CreateInBatches (写入) │
     ▼         │  └─ Raw → RawBuilder (原始SQL) │
┌─────────────────────┐
│  ShardTableManager    │  ← Facade：分片名 → 完整表名
│  ├─ CreateShardTable  │
│  ├─ CreateShardTableLike │
│  └─ LoadTemplate*       │
└──────────┬──────────┘
           │
           ▼
┌─────────────────────┐
│    TableManager       │  ← 数据库无关接口
│  ├─ MySQLTableManager │
│  ├─ PostgresTableManager │
│  └─ ClickHouseTableManager │
└─────────────────────┘
```

### 两条查询路径对比

| 特性 | Find（UNION ALL） | FindParallel（并行分片） |
|------|-------------------|--------------------------|
| 实现 | 单条 UNION SQL | 每分片独立查 |
| 分片数多的性能 | 差（SQL 过长） | 好（并发） |
| ORDER BY + LIMIT | 自然支持 | 应用层合并 |
| GROUP BY / HAVING | 支持 | 不支持 |
| 聚合方法 | — | Aggregate/Sum/Min/Max/Avg 自动合并 |
| 适用场景 | 分片数 < 10，需要 ORDER BY + LIMIT 自然分页 | 分片数 > 10，或希望充分利用并发 |

### 7 种分片策略总览

| 常量 | 值 | 表名格式 | 典型场景 | 支持 Scheduler 提前建表 |
|------|-----|---------|---------|----------------------|
| `ByYear` | `"year"` | `{base}_{YYYY}` | 归档历史数据 | ✅ |
| `ByQuarter` | `"quarter"` | `{base}_{YYYY}Q{n}` | 报表类数据 | ✅ |
| `ByMonth` | `"month"` | `{base}_{YYYY}{MM}` | 订单/交易（最常用） | ✅ |
| `ByWeek` | `"week"` | `{base}_{YYYY}W{WW}` | 短周期高频数据 | ✅ |
| `ByTenDay` | `"ten_day"` | `{base}_{YYYY}{MM}_{1\|2\|3}` | 月末月初热度不均 | ✅ |
| `ByIDHash` | `"id_hash"` | `{base}_{0..N-1}` | 均匀分布的写多型 | ✅（枚举全部） |
| `ByIDRange` | `"id_range"` | `{base}_{n}` | 按业务 ID 段预分配 | ❌ |

---

## 2. SonyflakeIDGenerator — 分布式 ID

基于 Sonyflake 改造的全局唯一 ID 生成器。

### ID 位段布局

```
| 1 bit (保留) | 39 bit 时间戳 (10ms 精度) | 16 bit 机器 ID | 8 bit 序列号 |
```

- **39 位时间戳**：从 `startTime` 起算，覆盖约 17 年
- **16 位机器 ID**：支持 65536 个节点
- **8 位序列号**：同一机器 10ms 内可生成 256 个 ID

### 机器 ID 自动发现优先级

1. 环境变量 `SONYFLAKE_MACHINE_ID`（显式指定，推荐容器化部署）
2. Pod IP 的后两个字节（Kubernetes 环境自动分配）
3. Hostname 哈希取模（兜底方案）

### 类型定义

```go
// SonyflakeIDGenerator 分布式 ID 生成器实例
type SonyflakeIDGenerator struct {
    flake     *sonyflake.Sonyflake
    startTime time.Time
    machineID uint16
}

// IDInfo Parse 返回的解析结果
type IDInfo struct {
    ID        uint64    // 原始 ID
    Timestamp time.Time // ID 生成时的时间戳（基于生成器 startTime 还原）
    MachineID uint16    // 机器 ID（16 位）
    Sequence  uint16    // 序列号（8 位）
}
```

### 实例方法

#### NewSonyflakeIDGenerator

```go
func NewSonyflakeIDGenerator(startTime time.Time) (*SonyflakeIDGenerator, error)
```

创建并初始化一个 Sonyflake ID 生成器。**同一集群内所有节点必须使用相同的 `startTime`**，否则 Parse 还原出的时间会错位。

**参数**：
- `startTime` `time.Time`：ID 起始时间，建议设为应用正式上线日，从此刻起算 39 位时间戳可覆盖约 17 年

**返回值**：
- `*SonyflakeIDGenerator`：生成器实例
- `error`：机器 ID 解析失败或底层 sonyflake 初始化失败时返回

**示例**：

```go
start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
gen, err := sharding.NewSonyflakeIDGenerator(start)
if err != nil {
    log.Fatal("初始化 ID 生成器失败:", err)
}
```

#### Generate

```go
func (g *SonyflakeIDGenerator) Generate() (uint64, error)
```

生成一个新的全局唯一 ID。线程安全，内部由 sonyflake 的原子计数器保证序列递增。

**返回值**：
- `uint64`：生成的 ID
- `error`：sonyflake 内部错误

**示例**：

```go
id, err := gen.Generate()
if err != nil {
    log.Fatal(err)
}
fmt.Printf("生成 ID: %d\n", id)
```

#### Parse

```go
func (g *SonyflakeIDGenerator) Parse(id uint64) (*IDInfo, error)
```

解析一个已有的 Sonyflake ID，拆出时间戳、机器 ID、序列号。**逆向路由场景特别有用**：拿到一个 order ID，就能知道它属于哪台机器、何时生成。

**参数**：
- `id` `uint64`：64 位 Sonyflake ID（由 Generate 或 GenerateFromTimestamp 产生）

**返回值**：
- `*IDInfo`：解析结果
- `error`：解析过程错误

**示例**：

```go
info, _ := gen.Parse(189472839472)
fmt.Println("生成时间:", info.Timestamp)  // 精确到 10ms
fmt.Println("机器 ID:", info.MachineID)
fmt.Println("序列号:", info.Sequence)
```

#### GenerateFromTimestamp

```go
func (g *SonyflakeIDGenerator) GenerateFromTimestamp(ts time.Time, sequence uint16) (uint64, error)
```

使用指定时间戳和序列号合成一个 Sonyflake ID。主要用途：**历史数据迁移/回填场景**下，让旧数据也拥有符合当前 ID 规则的主键，从而能正确路由到时间分片表。

> **注意**：同一 `(ts, sequence, machineID)` 组合会产生相同 ID，调用方需确保唯一性。

**参数**：
- `ts` `time.Time`：指定的时间戳，必须 >= 生成器 startTime 且在 39 位时间戳可表示范围内
- `sequence` `uint16`：序列号，低 8 位有效（0~255）

**返回值**：
- `uint64`：合成的 Sonyflake ID
- `error`：ts 早于 startTime 或超出 39 位时间范围时返回

**示例**：

```go
// 为 2024-06-15 的历史订单生成 ID，确保它们路由到 orders_202406 分片表
historicalID, _ := gen.GenerateFromTimestamp(
    time.Date(2024, 6, 15, 10, 30, 0, 0, time.UTC), 42,
)
// → 对应的分片表
rule := sharding.NewShardRule(sharding.ByMonth, "orders")
table, _ := rule.GetTableNameByTime(time.Date(2024, 6, 15, 10, 30, 0, 0, time.UTC))
fmt.Println(table) // orders_202406
```

### 包级全局单例函数

#### InitSonyflake

```go
func InitSonyflake(startTime time.Time) error
```

初始化全局 Sonyflake ID 生成器（包级单例）。并发安全，只会生效一次。若已被其他代码以不同 startTime 初始化，将返回 error。**建议在应用启动最早阶段调用一次**，避免并发首次调用时因机器 ID 解析竞争导致行为不可预测。

**参数**：
- `startTime` `time.Time`：ID 起始时间

**返回值**：
- `error`：已用不同 startTime 初始化过时返回

**示例**：

```go
func init() {
    if err := sharding.InitSonyflake(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
        panic(err)
    }
}
```

#### GenerateID

```go
func GenerateID() (uint64, error)
```

使用全局单例生成器生成一个唯一 ID。如果全局生成器尚未初始化，会使用默认 startTime（2023-01-01 UTC）自动初始化。

**返回值**：
- `uint64`：生成的 ID
- `error`：初始化或生成失败时返回

**示例**：

```go
id, err := sharding.GenerateID()
if err != nil {
    log.Fatal(err)
}
_ = id // 直接使用
```

#### ParseID

```go
func ParseID(id uint64) (*IDInfo, error)
```

解析一个 Sonyflake ID，返回其组成部分。内部使用全局生成器的 startTime 还原时间戳；若全局生成器尚未初始化，会自动以默认值初始化。

**参数**：
- `id` `uint64`：64 位 Sonyflake ID

**返回值**：
- `*IDInfo`：解析结果
- `error`：初始化或解析失败时返回

**示例**：

```go
info, err := sharding.ParseID(189472839472)
if err != nil {
    log.Fatal(err)
}
fmt.Println("创建于:", info.Timestamp)
fmt.Println("机器:", info.MachineID)
```

#### GenerateIDFromTimestamp

```go
func GenerateIDFromTimestamp(ts time.Time, seq uint16) (uint64, error)
```

使用全局生成器按指定时间戳合成 Sonyflake ID。历史数据迁移场景的便捷函数。

**参数**：
- `ts` `time.Time`：指定时间戳
- `seq` `uint16`：序列号（低 8 位有效）

**返回值**：
- `uint64`：合成的 ID
- `error`：初始化或合成失败时返回

**示例**：

```go
id, _ := sharding.GenerateIDFromTimestamp(
    time.Date(2024, 6, 15, 0, 0, 0, 0, time.UTC), 0,
)
```

---

## 3. ShardRule — 分片规则与路由

`ShardRule` 是整个分片路由系统的**核心**。写入时用单点路由方法得到目标分片表；跨片查询时用范围枚举方法枚举出所有可能命中的分片表。两种方法的分片公式严格一致，**保证写入到某张表的数据，查询范围枚举一定包含这张表**。

### 类型定义

```go
// ShardType 分片策略枚举
type ShardType string

// ShardRule 分片规则核心结构
type ShardRule struct {
    Type       ShardType // 分片策略类型
    TableName  string    // 基础表名，作为分片表名的前缀
    HashShards int       // 哈希分片数量（仅 ByIDHash 使用），默认 16
    RangeSize  int64     // 每个范围分片的 ID 容量（仅 ByIDRange 使用），默认 1_000_000
}

// Option 函数式选项类型
type Option func(*ShardRule)

// TimeRange 时间闭区间 [Start, End]
type TimeRange struct {
    Start time.Time
    End   time.Time
}

// IDRange ID 闭区间 [Start, End]
type IDRange struct {
    Start int64
    End   int64
}
```

### 7 种分片策略常量

```go
const (
    ByYear    ShardType = "year"      // 按年分片，表名: {base}_{YYYY}
    ByQuarter ShardType = "quarter"   // 按季度分片，表名: {base}_{YYYY}Q{n}
    ByMonth   ShardType = "month"     // 按月分片，表名: {base}_{YYYY}{MM}
    ByWeek    ShardType = "week"      // 按周分片，表名: {base}_{YYYY}W{WW}（ISOWeek）
    ByTenDay  ShardType = "ten_day"   // 按旬分片，表名: {base}_{YYYY}{MM}_{1|2|3}（1-10/11-20/21-月底）
    ByIDHash  ShardType = "id_hash"   // 按 ID 哈希分片，表名: {base}_{0..HashShards-1}（crc32 取模）
    ByIDRange ShardType = "id_range"  // 按 ID 范围分片，表名: {base}_{n}（id / RangeSize）
)
```

### Option 函数

#### WithHashShards

```go
func WithHashShards(n int) Option
```

设置哈希分片数量（ByIDHash 策略专用）。传入 `n <= 0` 时不生效（使用默认值 16）。

**参数**：
- `n` `int`：分片数量

**返回值**：
- `Option`：可传给 NewShardRule 的函数式选项

**示例**：

```go
rule := sharding.NewShardRule(sharding.ByIDHash, "users", sharding.WithHashShards(32))
```

#### WithRangeSize

```go
func WithRangeSize(size int64) Option
```

设置范围分片大小（ByIDRange 策略专用）。传入 `size <= 0` 时不生效（使用默认值 1_000_000）。

**参数**：
- `size` `int64`：每个分片容纳的 ID 数量

**返回值**：
- `Option`：可传给 NewShardRule 的函数式选项

**示例**：

```go
rule := sharding.NewShardRule(sharding.ByIDRange, "orders", sharding.WithRangeSize(5000))
```

### 构造函数

#### NewShardRule

```go
func NewShardRule(shardType ShardType, tableName string, opts ...Option) *ShardRule
```

创建一个 ShardRule 实例。自动填充默认值：`HashShards = 16`，`RangeSize = 1_000_000`。对于非对应策略，这些字段不会被读取，不会造成问题。

**参数**：
- `shardType` `ShardType`：分片策略（ByYear / ByMonth / ByIDHash 等）
- `tableName` `string`：基础表名（"orders"、"users" 等，不含后缀）
- `opts` `...Option`：可选参数，ByIDHash 用 `WithHashShards(n)`，ByIDRange 用 `WithRangeSize(n)`

**返回值**：
- `*ShardRule`：初始化好的分片规则

**示例**：

```go
// 按月分片：写入 orders_202611, 查询 2026-10~2026-11 枚举出 orders_202610, orders_202611
rule1 := sharding.NewShardRule(sharding.ByMonth, "orders")

// 32 个哈希分片：写入 users_0 ~ users_31
rule2 := sharding.NewShardRule(sharding.ByIDHash, "users", sharding.WithHashShards(32))

// 每 5000 个 ID 一张表：写入 orders_0, orders_1, orders_2, ...
rule3 := sharding.NewShardRule(sharding.ByIDRange, "orders", sharding.WithRangeSize(5000))
```

### 单点路由方法（写入用）

#### GetTableNameByTime

```go
func (r *ShardRule) GetTableNameByTime(t time.Time) (string, error)
```

根据时间分片策略（ByYear / ByQuarter / ByMonth / ByWeek / ByTenDay）计算某个时间点应该落到哪张分片表。**不支持的分片类型会返回 error**。

**参数**：
- `t` `time.Time`：用于路由的时间点

**返回值**：
- `string`：分片表名
- `error`：分片类型不匹配时返回

**示例**：

```go
// ByMonth
rule := sharding.NewShardRule(sharding.ByMonth, "orders")
table, _ := rule.GetTableNameByTime(time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC))
fmt.Println(table) // orders_202611

// ByWeek（ISO 周编号）
rule2 := sharding.NewShardRule(sharding.ByWeek, "logs")
table2, _ := rule2.GetTableNameByTime(time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC))
fmt.Println(table2) // logs_2026W45

// ByYear
rule3 := sharding.NewShardRule(sharding.ByYear, "archive")
table3, _ := rule3.GetTableNameByTime(time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC))
fmt.Println(table3) // archive_2026

// ByQuarter
rule4 := sharding.NewShardRule(sharding.ByQuarter, "reports")
table4, _ := rule4.GetTableNameByTime(time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC))
fmt.Println(table4) // reports_2026Q2

// ByTenDay（旬分片：1-10/11-20/21-月底）
rule5 := sharding.NewShardRule(sharding.ByTenDay, "metrics")
table5, _ := rule5.GetTableNameByTime(time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC))
fmt.Println(table5) // metrics_202611_3
table6, _ := rule5.GetTableNameByTime(time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC))
fmt.Println(table6) // metrics_202611_2
table7, _ := rule5.GetTableNameByTime(time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC))
fmt.Println(table7) // metrics_202611_1
```

#### GetTableNameByID

```go
func (r *ShardRule) GetTableNameByID(id int64) (string, error)
```

根据 ID 分片策略（ByIDHash / ByIDRange）计算某个 ID 应该落到哪张分片表。

- **ByIDHash**：`crc32(id 十进制字符串) % HashShards`
- **ByIDRange**：`id / RangeSize`，保证相同范围写入同一张表

**参数**：
- `id` `int64`：业务数据的唯一 ID

**返回值**：
- `string`：分片表名
- `error`：分片类型不匹配时返回

**示例**：

```go
// ByIDHash
rule := sharding.NewShardRule(sharding.ByIDHash, "users", sharding.WithHashShards(32))
table, _ := rule.GetTableNameByID(123456789)
fmt.Println(table) // users_5（crc32(...) % 32）

// ByIDRange
rule2 := sharding.NewShardRule(sharding.ByIDRange, "orders", sharding.WithRangeSize(1000))
table2, _ := rule2.GetTableNameByID(2500)
fmt.Println(table2) // orders_2 （2500 / 1000 = 2）

table3, _ := rule2.GetTableNameByID(999)
fmt.Println(table3) // orders_0
```

### 范围枚举方法（跨片查询用）

#### GetTableNamesByTimeRange

```go
func (r *ShardRule) GetTableNamesByTimeRange(tr TimeRange) ([]string, error)
```

根据时间范围枚举出所有可能命中的分片表名。**枚举算法保证**：在 `[tr.Start, tr.End]` 区间内任意一天调用 `GetTableNameByTime` 的结果，一定在本方法返回的列表中。

**参数**：
- `tr` `TimeRange`：时间闭区间 `[Start, End]`

**返回值**：
- `[]string`：时间范围内涉及到的所有分片表名（按时间/分片顺序排列，不会重复）
- `error`：Start 晚于 End 或分片类型不匹配时返回

**示例**：

```go
rule := sharding.NewShardRule(sharding.ByMonth, "orders")

// 跨两个月
tables, _ := rule.GetTableNamesByTimeRange(sharding.TimeRange{
    Start: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
    End:   time.Date(2026, 11, 30, 23, 59, 59, 0, time.UTC),
})
fmt.Println(tables) // [orders_202610 orders_202611]

// 单月
tables2, _ := rule.GetTableNamesByTimeRange(sharding.TimeRange{
    Start: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
    End:   time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC),
})
fmt.Println(tables2) // [orders_202611]

// 按年分片，跨三年
yearRule := sharding.NewShardRule(sharding.ByYear, "archive")
tables3, _ := yearRule.GetTableNamesByTimeRange(sharding.TimeRange{
    Start: time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC),
    End:   time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
})
fmt.Println(tables3) // [archive_2024 archive_2025 archive_2026]
```

#### GetTableNamesByIDRange

```go
func (r *ShardRule) GetTableNamesByIDRange(idRange IDRange) ([]string, error)
```

根据 ID 范围枚举出所有可能命中的分片表名。

**智能枚举策略**：
- **ByIDRange**：计算 `id/RangeSize` 的最小值和最大值，枚举出中间所有分片（精准、高效）
- **ByIDHash**：
  - 单个 ID（Start == End > 0）时直接返回对应的哈希分片
  - 小范围（≤10 个 ID）逐个计算哈希去重
  - 大范围（>10 个 ID）直接返回全部 HashShards 张表（因为 crc32 无法根据范围做有效过滤）

**参数**：
- `idRange` `IDRange`：ID 闭区间 `[Start, End]`

**返回值**：
- `[]string`：范围内涉及到的分片表名
- `error`：Start > End 或分片类型不匹配时返回

**示例**：

```go
// ByIDRange：RangeSize=1000，查 50~250 实际命中 shard 0 和 shard 1
rule := sharding.NewShardRule(sharding.ByIDRange, "orders", sharding.WithRangeSize(1000))
tables, _ := rule.GetTableNamesByIDRange(sharding.IDRange{Start: 50, End: 250})
fmt.Println(tables) // [orders_0 orders_1]

// 查 1500~2500 命中 shard 1 和 shard 2
tables2, _ := rule.GetTableNamesByIDRange(sharding.IDRange{Start: 1500, End: 2500})
fmt.Println(tables2) // [orders_1 orders_2]

// ByIDHash：大范围永远返回全部分片
hashRule := sharding.NewShardRule(sharding.ByIDHash, "users", sharding.WithHashShards(8))
tables3, _ := hashRule.GetTableNamesByIDRange(sharding.IDRange{Start: 1, End: 1000000})
fmt.Println(tables3) // [users_0 users_1 ... users_7]，全部 8 张

// ByIDHash：小范围逐个哈希去重
tables4, _ := hashRule.GetTableNamesByIDRange(sharding.IDRange{Start: 100, End: 105})
// 返回命中的哈希分片子集（不超过 6 张，按实际哈希结果）

// ByIDHash：单个 ID 直接返回对应分片
tables5, _ := hashRule.GetTableNamesByIDRange(sharding.IDRange{Start: 12345, End: 12345})
fmt.Println(tables5) // 只有一张，如 [users_4]
```

---

## 4. TableManager 接口 — 数据库无关的表操作抽象

`TableManager` 是不同数据库的"表级别操作"抽象接口。三种实现：MySQLTableManager / PostgresTableManager / ClickHouseTableManager。

**所有方法对动态表名都会执行白名单校验**（仅字母/数字/下划线），防止表名注入。

### 接口定义

```go
type TableManager interface {
    // TableExists 检查表是否存在
    TableExists(ctx context.Context, table string) (bool, error)

    // CreateTable 使用原始 DDL SQL 创建新表
    // MySQL 实现会自动处理 SQL 中硬编码的旧表名 → 新表名，并添加 IF NOT EXISTS
    // Postgres/ClickHouse 直接执行传入的 SQL（调用方负责完整性）
    CreateTable(ctx context.Context, table, sql string) error

    // CreateTableLike 克隆 sourceTable 的完整结构（含索引、约束），创建为 newTable
    CreateTableLike(ctx context.Context, newTable, sourceTable string) error

    // DropTable 删除表（IF EXISTS，幂等）
    DropTable(ctx context.Context, table string) error

    // ListTables 列出所有以 prefix 开头的表（用于枚举一个逻辑实体的所有分片）
    ListTables(ctx context.Context, prefix string) ([]string, error)
}
```

### 方法签名汇总

| 方法 | 签名 | 说明 |
|------|------|------|
| `TableExists` | `(ctx, table) → (bool, error)` | 检查表是否存在 |
| `CreateTable` | `(ctx, table, sql) → error` | 用 DDL 创建新表 |
| `CreateTableLike` | `(ctx, newTable, sourceTable) → error` | 克隆源表结构 |
| `DropTable` | `(ctx, table) → error` | 删除表 |
| `ListTables` | `(ctx, prefix) → ([]string, error)` | 按前缀枚举所有表 |

### 数据库实现构造函数

| 函数 | 数据库 | 文件 |
|------|--------|------|
| `NewMySQLTableManager(db *gorm.DB) *MySQLTableManager` | MySQL | `mysql_table_manager.go` |
| `NewPostgresTableManager(db *gorm.DB) *PostgresTableManager` | PostgreSQL | `postgres_table_manager.go` |
| `NewClickHouseTableManager(db *gorm.DB) *ClickHouseTableManager` | ClickHouse | `clickhouse_table_manager.go` |

### MySQL 使用示例

```go
import (
    "context"
    "time"

    "gorm.io/gorm"
    sharding "meteor-forge/pkg/sharding"
)

db, _ := gorm.Open(mysql.Open("..."), &gorm.Config{})
mgr := sharding.NewMySQLTableManager(db)
ctx := context.Background()

// 检查表是否存在
exists, _ := mgr.TableExists(ctx, "orders_202611")
if !exists {
    // 方式 1：用 CreateTableLike 从模板表克隆（推荐）
    mgr.CreateTableLike(ctx, "orders_202611", "orders_template")

    // 方式 2：用 CreateTable + DDL
    mgr.CreateTable(ctx, "orders_202611", `
        CREATE TABLE orders (
            id BIGINT UNSIGNED PRIMARY KEY,
            user_id BIGINT NOT NULL,
            amount DECIMAL(10,2),
            created_at DATETIME DEFAULT CURRENT_TIMESTAMP
        ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4
    `)
}

// 枚举所有 orders 分片表
tables, _ := mgr.ListTables(ctx, "orders_")
fmt.Println(tables) // [orders_202609 orders_202610 orders_202611]

// 删除过期分片
mgr.DropTable(ctx, "orders_202401")
```

### PostgreSQL 使用示例

```go
db, _ := gorm.Open(postgres.Open("..."), &gorm.Config{})
mgr := sharding.NewPostgresTableManager(db)
ctx := context.Background()

// 检查表是否存在
exists, _ := mgr.TableExists(ctx, "orders_202611")
if !exists {
    // 用 CREATE TABLE ... LIKE ... INCLUDING ALL 克隆（Postgres 特有）
    // 如果主 DDL 不支持，会自动 fallback 到 CREATE TABLE ... AS TABLE ... WITH NO DATA
    mgr.CreateTableLike(ctx, "orders_202611", "orders_template")
}

// 直接执行 DDL
mgr.CreateTable(ctx, "orders_202612", `
    CREATE TABLE IF NOT EXISTS orders_202612 (
        id BIGSERIAL PRIMARY KEY,
        user_id BIGINT NOT NULL,
        amount NUMERIC(10,2),
        created_at TIMESTAMP DEFAULT NOW()
    )
`)
```

### ClickHouse 使用示例

```go
db, _ := gorm.Open(clickhouse.Open("..."), &gorm.Config{})
mgr := sharding.NewClickHouseTableManager(db)
ctx := context.Background()

// ClickHouse 要求 DDL 必须显式包含 ENGINE 和 ORDER BY
mgr.CreateTable(ctx, "events_202611", `
    CREATE TABLE events_202611 (
        id UInt64,
        user_id UInt64,
        event_type String,
        created_at DateTime
    ) ENGINE = MergeTree()
    ORDER BY (user_id, created_at)
`)

// 克隆结构
mgr.CreateTableLike(ctx, "events_202612", "events_template")
```

---

## 5. ShardTableManager — 分片表管理 Facade

`ShardTableManager` 是一个外观（Facade）结构：把底层的 `TableManager` 和一个分片名 → 完整表名的格式化函数组合起来，**让调用方只需要处理分片名**（如 `"202611"`、`"7"`），不用关心完整表名的拼装细节。

与 `Scheduler` 配合使用时，`Scheduler` 内部调 `ShardTableManager` 的方法来创建未来的分片表。

### 类型定义

```go
type ShardTableManager struct {
    manager    TableManager
    baseName   string
    schemaTpl  string
    nameFormat func(string) string
}
```

### 构造函数

#### NewShardTableManager

```go
func NewShardTableManager(manager TableManager, baseName, schemaTpl string, fn func(string) string) *ShardTableManager
```

**参数**：
- `manager` `TableManager`：底层 TableManager 实现
- `baseName` `string`：基础表名前缀（如 "orders"），用于 `ListShardTables` 等枚举操作
- `schemaTpl` `string`：CREATE TABLE DDL 模板（支持 `LoadTemplateFromFile` / `LoadTemplateFromString` 覆盖）
- `fn` `func(string) string`：分片名 → 完整表名的格式化函数；传 `nil` 时退化为直接用 shardName 作为表名

**返回值**：
- `*ShardTableManager`

**示例**：

```go
mgr := sharding.NewMySQLTableManager(db)

// 方式 A：内联 SQL 模板
stm := sharding.NewShardTableManager(mgr, "orders", `
    CREATE TABLE orders (
        id BIGINT PRIMARY KEY,
        user_id BIGINT NOT NULL,
        amount DECIMAL(10,2),
        created_at DATETIME,
        INDEX idx_user (user_id)
    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4
`, func(shardName string) string {
    return "orders_" + shardName
})

// 方式 B：无模板（稍后 LoadTemplateFromFile）
stm2 := sharding.NewShardTableManager(mgr, "orders", "", func(shardName string) string {
    return "orders_" + shardName
})
stm2.LoadTemplateFromFile("./templates/orders.sql")
```

### 表管理方法

#### CreateShardTable

```go
func (m *ShardTableManager) CreateShardTable(ctx context.Context, shardName string) error
```

用已加载的 `schemaTpl` 创建一个分片表。`nameFormat(shardName)` 会被注入到 SQL 模板中。

**参数**：
- `ctx` `context.Context`
- `shardName` `string`：分片名（如 "202611"、"5"）

**返回值**：
- `error`：创建失败时返回

**示例**：

```go
ctx := context.Background()
stm.CreateShardTable(ctx, "202611") // 用模板建 orders_202611
stm.CreateShardTable(ctx, "202612") // 建 orders_202612

// 对于 ByIDHash 策略（分片名是数字）
stm3 := sharding.NewShardTableManager(mgr, "users", schema, func(s string) string {
    return "users_" + s
})
stm3.CreateShardTable(ctx, "0")  // users_0
stm3.CreateShardTable(ctx, "31") // users_31
```

#### CreateShardTableLike

```go
func (m *ShardTableManager) CreateShardTableLike(ctx context.Context, newShardName, sourceShardName string) error
```

克隆 `sourceShard` 的结构创建 `newShard`（含索引、约束）。适用于模板表已经存在时快速创建新分片的场景。

**参数**：
- `ctx` `context.Context`
- `newShardName` `string`：新分片名
- `sourceShardName` `string`：源分片名（已存在的分片）

**返回值**：
- `error`：创建失败时返回

**示例**：

```go
ctx := context.Background()
// 从 orders_202611 克隆结构创建 orders_202612
stm.CreateShardTableLike(ctx, "202612", "202611")
```

#### DropShardTable

```go
func (m *ShardTableManager) DropShardTable(ctx context.Context, shardName string) error
```

删除一个分片表（`IF EXISTS`，幂等）。

**参数**：
- `ctx` `context.Context`
- `shardName` `string`：分片名

**返回值**：
- `error`：删除失败时返回

**示例**：

```go
ctx := context.Background()
stm.DropShardTable(ctx, "202401") // 清理过期分片 orders_202401
```

#### TableExists

```go
func (m *ShardTableManager) TableExists(ctx context.Context, shardName string) (bool, error)
```

检查一个分片表是否存在。

**参数**：
- `ctx` `context.Context`
- `shardName` `string`：分片名

**返回值**：
- `bool`：是否存在
- `error`：检查失败时返回

**示例**：

```go
ctx := context.Background()
ok, _ := stm.TableExists(ctx, "202611")
if ok {
    fmt.Println("orders_202611 已存在")
} else {
    stm.CreateShardTable(ctx, "202611")
}
```

#### ListShardTables

```go
func (m *ShardTableManager) ListShardTables(ctx context.Context) ([]string, error)
```

列出数据库中所有以 `baseName` 开头的分片表。可用于运维监控（当前有多少分片、最早/最晚的分片是什么）。

**参数**：
- `ctx` `context.Context`

**返回值**：
- `[]string`：分片表名列表
- `error`：枚举失败时返回

**示例**：

```go
ctx := context.Background()
tables, _ := stm.ListShardTables(ctx)
fmt.Println("当前分片:", tables) // [orders_202401 orders_202402 ... orders_202611]
fmt.Printf("共 %d 个分片\n", len(tables))
```

### 模板管理方法

#### LoadTemplateFromFile

```go
func (m *ShardTableManager) LoadTemplateFromFile(filePath string) error
```

从磁盘读取 CREATE TABLE 模板。启动时可以先编译好模板文件，再加载到 ShardTableManager。

**参数**：
- `filePath` `string`：SQL 模板文件路径（支持相对/绝对路径）

**返回值**：
- `error`：文件不存在、无法读取或内容为空时返回

**示例**：

```go
stm.LoadTemplateFromFile("./templates/orders.sql")
stm.LoadTemplateFromFile("/etc/app/shard_templates/users.sql")
```

#### LoadTemplateFromString

```go
func (m *ShardTableManager) LoadTemplateFromString(templateStr string) error
```

直接以字符串形式设置 CREATE TABLE 模板。

**参数**：
- `templateStr` `string`：DDL 模板字符串

**返回值**：
- `error`：templateStr 为空时返回

**示例**：

```go
stm.LoadTemplateFromString(`
    CREATE TABLE orders (
        id BIGINT PRIMARY KEY,
        user_id BIGINT NOT NULL,
        created_at DATETIME
    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4
`)
```

#### GetCurrentTemplate

```go
func (m *ShardTableManager) GetCurrentTemplate() string
```

返回当前已加载的 DDL 模板字符串。主要用于调试或日志记录。

**返回值**：
- `string`：DDL 模板字符串（未加载时返回空串）

**示例**：

```go
tpl := stm.GetCurrentTemplate()
log.Println("当前模板:", tpl)
```

---

## 6. Scheduler — 定时提前建表

后台协程定期检查并创建未来缺失的分片表。适合提前按月/按年建好下一个周期的表，**避免业务高峰时因首次建表阻塞写入**。

### 支持范围

| 分片类型 | 是否支持提前建表 | 算法 |
|---------|----------------|------|
| `ByYear` | ✅ | 提前 `ceil(monthsAhead/12)+2` 年 |
| `ByQuarter` | ✅ | 提前 `ceil(monthsAhead/3)+2` 季度 |
| `ByMonth` | ✅ | 提前 `monthsAhead+2` 月 |
| `ByWeek` | ✅ | 提前 `monthsAhead×5+2` 周 |
| `ByTenDay` | ✅ | 提前 `monthsAhead×3+2` 段 |
| `ByIDHash` | ✅ | 枚举全部 `HashShards` 个分片（固定数量） |
| `ByIDRange` | ❌ | 不支持——分片取决于运行时业务 ID，无法预建 |

> 每种时间分片类型都额外 `+2` 的安全余量，避免边界时刻（如月末最后一天）因计算误差漏建。

### 构造函数

#### NewScheduler

```go
func NewScheduler(manager *ShardTableManager, shardRule *ShardRule, interval time.Duration, monthsAhead int) *Scheduler
```

**参数**：
- `manager` `*ShardTableManager`：ShardTableManager 实例，用于检查/创建分片表
- `shardRule` `*ShardRule`：ShardRule 实例，决定了分片表的命名规则
- `interval` `time.Duration`：定时检查间隔（如 `24*time.Hour` 表示每天检查一次）
- `monthsAhead` `int`：提前多少个月创建分片表（适用于所有时间分片类型的换算）

**返回值**：
- `*Scheduler`

**示例**：

```go
// 按月分片：每天检查，提前 6 个月（实际会建 monthsAhead+2=8 个月的表）
scheduler := sharding.NewScheduler(stm, rule, 24*time.Hour, 6)

// 按年分片：每小时检查，提前 24 个月
yearScheduler := sharding.NewScheduler(stm, yearRule, time.Hour, 24)
// 提前 ceil(24/12)+2 = 4 年
```

### 生命周期方法

#### Start

```go
func (s *Scheduler) Start()
```

启动调度器。内部会先**立即执行一次**建表检查（不等到下一次 tick），然后按 `interval` 周期运行。**非阻塞调用**，内部起 goroutine。

**示例**：

```go
scheduler.Start()
// 启动后立即创建缺失的分片表，然后每天 00:00 再检查一次
```

#### Stop

```go
func (s *Scheduler) Stop()
```

停止调度器。会等待内部 goroutine 结束再返回，**应在应用退出时调用**（配合 `defer`）。

**示例**：

```go
scheduler := sharding.NewScheduler(stm, rule, 24*time.Hour, 6)
scheduler.Start()
defer scheduler.Stop() // 应用退出时优雅停止
```

### 辅助查询方法

#### GetCurrentShardTable

```go
func (s *Scheduler) GetCurrentShardTable() (string, error)
```

获取"当前"分片表的表名。对于 ByMonth 就是当月的表，ByQuarter 就是当季的表，依此类推。ID 分片类型（ByIDHash / ByIDRange）返回空字符串和 nil。

**返回值**：
- `string`：当前分片表名（如 `"orders_202611"`）
- `error`：计算失败或不支持的分片类型

**示例**：

```go
current, _ := scheduler.GetCurrentShardTable()
log.Println("当前分片表:", current) // orders_202611（假设当前是 2026 年 11 月）
```

#### GetNextShardTable

```go
func (s *Scheduler) GetNextShardTable() (string, error)
```

获取"下一个"分片表的表名。对于 ByMonth 就是下个月的表，ByQuarter 就是下季度的表，依此类推。ID 分片类型返回空字符串和 nil。

**返回值**：
- `string`：下一个分片表名
- `error`：计算失败或不支持的分片类型

**示例**：

```go
next, _ := scheduler.GetNextShardTable()
log.Println("下一个分片表:", next) // orders_202612（假设当前是 2026 年 11 月）
```

#### ListAllShardTables

```go
func (s *Scheduler) ListAllShardTables() ([]string, error)
```

列出数据库中**实际存在**的所有分片表（以 `baseName` 开头）。与 `GetCurrentShardTable` / `GetNextShardTable`（**计算出来**的预期表名）不同，这里返回的是数据库里真实存在的。

**返回值**：
- `[]string`：实际存在的分片表名列表
- `error`：查询失败时返回

**示例**：

```go
tables, _ := scheduler.ListAllShardTables()
log.Println("现有分片表:", tables) // [orders_202401 ... orders_202611]
log.Printf("共 %d 张表\n", len(tables))
```

---

## 7. QueryBuilder — 跨分片查询

基于 gorm 的链式查询构建器，支持两种跨片查询模式：

### 构造函数

#### NewQueryBuilder

```go
func NewQueryBuilder(db *gorm.DB, rule *ShardRule) *QueryBuilder
```

**参数**：
- `db` `*gorm.DB`：gorm.DB 实例
- `rule` `*ShardRule`：分片规则。`CreateInBatches` 写入场景需要它来路由；纯查询场景如果手动 `Tables(...)` 指定了表，`rule` 可以为 nil

**返回值**：
- `*QueryBuilder`

**示例**：

```go
// 带 rule 的完整 QueryBuilder（支持查询 + 写入）
qb := sharding.NewQueryBuilder(db, rule)

// 纯查询场景，手动指定表，rule 可以为 nil
qb2 := sharding.NewQueryBuilder(db, nil)
qb2.Tables("orders_202610", "orders_202611")
```

### 链式构建方法

#### WithContext

```go
func (qb *QueryBuilder) WithContext(ctx context.Context) *QueryBuilder
```

设置查询的 context。会传播到所有分片级别的查询（FindParallel / CountParallel / First 等），用于超时控制或取消。

**参数**：
- `ctx` `context.Context`

**返回值**：
- `*QueryBuilder`：链式返回

**示例**：

```go
ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
defer cancel()
qb.WithContext(ctx).FindParallel(&results, 8)
```

#### Tables

```go
func (qb *QueryBuilder) Tables(tables ...string) *QueryBuilder
```

手动指定目标分片表。与 `TimeRange` / `IDRange` 互斥——后者会自动枚举并追加到这里。

**参数**：
- `tables` `...string`：分片表名列表

**返回值**：
- `*QueryBuilder`：链式返回

**示例**：

```go
qb.Tables("orders_202610", "orders_202611").Find(&results)
```

#### TimeRange

```go
func (qb *QueryBuilder) TimeRange(tr TimeRange) *QueryBuilder
```

根据时间分片规则自动枚举出 `[Start, End]` 区间覆盖的所有分片表。内部调 `ShardRule.GetTableNamesByTimeRange`。适用于 ByYear / ByQuarter / ByMonth / ByWeek / ByTenDay 等时间分片策略。传 ID 分片规则时会静默跳过（tables 不变）。

**参数**：
- `tr` `TimeRange`：时间闭区间

**返回值**：
- `*QueryBuilder`：链式返回

**示例**：

```go
qb.TimeRange(sharding.TimeRange{
    Start: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
    End:   time.Date(2026, 11, 30, 23, 59, 59, 0, time.UTC),
})
// 自动追加 orders_202610 和 orders_202611
```

#### IDRange

```go
func (qb *QueryBuilder) IDRange(idr IDRange) *QueryBuilder
```

根据 ID 分片规则自动枚举出覆盖 `[Start, End]` 区间的分片表。内部调 `ShardRule.GetTableNamesByIDRange`。ByIDRange 策略会精准计算命中分片；ByIDHash 策略在大范围时返回全部分片。

**参数**：
- `idr` `IDRange`：ID 闭区间

**返回值**：
- `*QueryBuilder`：链式返回

**示例**：

```go
qb.IDRange(sharding.IDRange{Start: 1000, End: 5000})
// 对于 ByIDRange + RangeSize=1000 → orders_1 orders_2 orders_3 orders_4 orders_5
```

#### ExcludeTables

```go
func (qb *QueryBuilder) ExcludeTables(tables ...string) *QueryBuilder
```

从查询集中排除指定的分片表。通常与 TimeRange/IDRange 组合使用，跳过重建中或有问题的表。

**参数**：
- `tables` `...string`：要排除的分片表名

**返回值**：
- `*QueryBuilder`：链式返回

**示例**：

```go
qb.TimeRange(tr).ExcludeTables("orders_202609").Find(&results)
// 2026-10~11 的分片表中排除 orders_202609（TimeRange 没枚举它，但显式排除更安全）
```

#### Select

```go
func (qb *QueryBuilder) Select(columns ...string) *QueryBuilder
```

指定要查询的列。不调用则默认 `SELECT *`。

**参数**：
- `columns` `...string`：列名列表

**返回值**：
- `*QueryBuilder`：链式返回

**示例**：

```go
qb.Select("id", "user_id", "amount")
```

#### Where

```go
func (qb *QueryBuilder) Where(query string, args ...interface{}) *QueryBuilder
```

添加 AND 连接的 WHERE 条件。链式多次调用会自动用 AND 组合；如果需要 OR 语义用 `OrWhere`。

> **安全机制**：如果 query 中 `?` 的数量与 args 数量不匹配会 **panic**。这是刻意设计——链式 Builder 的编程错误在开发时就暴露，避免静默产生错误 SQL。

**参数**：
- `query` `string`：条件字符串（支持 `?` 占位符）
- `args` `...interface{}`：占位符参数

**返回值**：
- `*QueryBuilder`：链式返回

**示例**：

```go
qb.Where("status = ?", 1).
   Where("amount > ?", 100).
   Where("user_id IN ?", []int64{1, 2, 3}).
   Find(&results)

// 上面组合成: WHERE (status = ?) AND (amount > ?) AND (user_id IN ?)
```

#### OrWhere

```go
func (qb *QueryBuilder) OrWhere(query string, args ...interface{}) *QueryBuilder
```

添加 OR 连接的条件。安全机制同 Where——占位符数量不匹配会 panic。

**参数**：
- `query` `string`：条件字符串
- `args` `...interface{}`：占位符参数

**返回值**：
- `*QueryBuilder`：链式返回

**示例**：

```go
qb.Where("status = ?", 1).
   OrWhere("status = ?", 2)
// → WHERE (status = ?) OR (status = ?)
```

#### Order

```go
func (qb *QueryBuilder) Order(order string) *QueryBuilder
```

设置 ORDER BY 子句。字符串会原样拼到 SQL 中，请确保内容安全（通常为硬编码）。

**参数**：
- `order` `string`：排序表达式

**返回值**：
- `*QueryBuilder`：链式返回

**示例**：

```go
qb.Order("created_at DESC")
qb.Order("amount DESC, id ASC")
```

#### Group

```go
func (qb *QueryBuilder) Group(columns ...string) *QueryBuilder
```

设置 GROUP BY 列。

**参数**：
- `columns` `...string`：GROUP BY 列名

**返回值**：
- `*QueryBuilder`：链式返回

**示例**：

```go
qb.Group("user_id", "status")
// → GROUP BY user_id, status
```

#### Having

```go
func (qb *QueryBuilder) Having(having string, args ...interface{}) *QueryBuilder
```

添加 HAVING 子句，通常与 Group 配合使用。安全机制同 Where——占位符数量不匹配会 panic。

**参数**：
- `having` `string`：HAVING 表达式
- `args` `...interface{}`：占位符参数

**返回值**：
- `*QueryBuilder`：链式返回

**示例**：

```go
qb.Group("user_id").Having("COUNT(*) > ?", 5)
```

#### Limit / Offset

```go
func (qb *QueryBuilder) Limit(limit int) *QueryBuilder
func (qb *QueryBuilder) Offset(offset int) *QueryBuilder
```

设置分页。适用于 Find（UNION ALL 自动 LIMIT 下推到子查询）。FindParallel 的并行路径需要调用方自己对结果切片做分页。

**参数**：
- `limit` `int`：返回的最大行数
- `offset` `int`：跳过的行数

**返回值**：
- `*QueryBuilder`：链式返回

**示例**：

```go
qb.Limit(20).Offset(40) // 第 3 页，每页 20 条
```

### 查询执行方法

#### First

```go
func (qb *QueryBuilder) First(dest interface{}) error
```

从一个或多个分片表中查询一条记录。

**优化策略**：
- 无 ORDER BY → **逐分片扫描，命中即返回**（避免 UNION ALL 全表扫描）
- 有 ORDER BY → 必须用 UNION ALL（因为"全局排序后的第一条"无法分片确定）

**参数**：
- `dest` `interface{}`：目标对象（单个 struct 或 map）

**返回值**：
- `error`：查询失败或 `gorm.ErrRecordNotFound`

**示例**：

```go
var order Order
qb.TimeRange(tr).Where("id = ?", 12345).First(&order)
// 如果 id 路由到 orders_202610，只会查 orders_202610 这一张表

// 带 ORDER BY → UNION ALL
var latestOrder Order
qb.TimeRange(tr).Where("status = ?", 1).Order("created_at DESC").First(&latestOrder)
```

#### Find

```go
func (qb *QueryBuilder) Find(dest interface{}) error
```

查询多条记录。单表时直接 GORM 查询；多表时 **UNION ALL 合成一条 SQL**，由数据库合并并在最外层做 ORDER BY / LIMIT / GROUP BY / HAVING。

**适用场景**：分片数 < 10，需要 ORDER BY + LIMIT 自然分页。分片数多时改用 FindParallel。

**参数**：
- `dest` `interface{}`：目标切片（`*[]Order` 等）

**返回值**：
- `error`：查询失败时返回

**示例**：

```go
var orders []Order
qb.TimeRange(tr).
   Where("status = ?", 1).
   Order("created_at DESC").
   Limit(20).
   Find(&orders)
// 生成: SELECT * FROM (
//   (SELECT * FROM orders_202610 WHERE status = ? ORDER BY created_at DESC LIMIT 20)
//   UNION ALL
//   (SELECT * FROM orders_202611 WHERE status = ? ORDER BY created_at DESC LIMIT 20)
// ) AS union_result ORDER BY created_at DESC LIMIT 20
```

#### FindParallel

```go
func (qb *QueryBuilder) FindParallel(dest interface{}, concurrency int) error
```

**并行查询每个分片**，然后在应用层合并结果。

**参数**：
- `dest` `interface{}`：必须是 `*[]T`（指向切片的指针）
- `concurrency` `int`：控制并发 goroutine 数，`<=0` 或 `>=len(tables)` 表示全并发

**约束**：不支持 GROUP BY / HAVING。

**返回值**：
- `error`：查询失败时返回

**示例**：

```go
var users []User
qb.Where("status = ? AND age > ?", 1, 18).
   FindParallel(&users, 8)
// concurrency=8：最多 8 个分片同时查询
// 自动：合并 + ORDER BY 排序（如果有）+ 分页（如果有）
```

#### FindInBatches

```go
func (qb *QueryBuilder) FindInBatches(dest interface{}, batchSize int, callback func(batch int64) bool) error
```

**大结果集分批迭代器**。按分片顺序，每个分片按 `batchSize` 分页拉取，直到所有分片处理完毕。`callback` 返回 `false` 或 ctx 取消时提前终止。这避免一次性把所有分片的结果加载到内存。

> **注意**：`batchSize` 必须 > 0。不支持 ORDER BY（分片内分批无全局排序语义）。

**参数**：
- `dest` `interface{}`：目标切片（`*[]Order` 等）
- `batchSize` `int`：每批拉取的大小
- `callback` `func(batch int64) bool`：每批数据处理回调。返回 `true` 继续下一批，返回 `false` 提前终止

**返回值**：
- `error`：查询失败时返回

**示例**：

```go
var batch []Order
qb.TimeRange(tr).
   Where("status = ?", 1).
   FindInBatches(&batch, 500, func(batchNum int64) bool {
       fmt.Printf("处理第 %d 批，共 %d 条\n", batchNum, len(batch))
       for _, o := range batch {
           processOrder(o)
       }
       return true // 继续下一批，返回 false 提前终止
   })
```

### 聚合方法

#### Aggregate

```go
func (qb *QueryBuilder) Aggregate(dest interface{}, aggregateExpr string) error
```

通用聚合方法。每个分片独立执行聚合查询，然后**自动合并结果**。

- **无 GROUP BY**：每分片返回单行聚合结果，按字段名后缀（_cnt/_sum/_min/_max/_avg）自动合并
- **有 GROUP BY**：每分片独立聚合后，按 GROUP BY 字段 key 合并

**参数**：
- `dest` `interface{}`：指向切片的指针，每个元素是 struct/map，字段名与 aggregateExpr 的别名匹配
- `aggregateExpr` `string`：完整的 SELECT 子句

**合并规则**（按字段名后缀匹配）：

| 后缀 | 合并动作 |
|------|---------|
| `_cnt` / `count` / `total_cnt` / `rows` / `*Count` | 累加 |
| `_sum` / `sum` / `total` / `total_sum` / `amount` | 累加 |
| `_min` / `min` | 取最小 |
| `_max` / `max` | 取最大 |
| 其他 | 保留第一个值 |

**示例**：

```go
// 单聚合：SUM(amount) AS total — 分片自动求和
type TotalRow struct { Total float64 }
var rows []TotalRow
qb.TimeRange(tr).Aggregate(&rows, "SUM(amount) AS total")

// 多聚合 + GROUP BY
type StatRow struct {
    Status int32   `gorm:"column:status"`
    Total  float64 `gorm:"column:total"`
    Cnt    int64   `gorm:"column:cnt"`
}
var stats []StatRow
qb.Group("status").Aggregate(&stats,
    "status, SUM(amount) AS total, COUNT(*) AS cnt")
// 合并策略：同一 status key 的分片结果，total 累加、cnt 累加
```

#### Sum

```go
func (qb *QueryBuilder) Sum(column string, dest interface{}) error
```

便捷聚合：`SELECT SUM(column) AS <column>_sum FROM ...`。分片自动求和。

**参数**：
- `column` `string`：列名
- `dest` `interface{}`：`*int64` 或 `*float64` 等数值指针

**返回值**：
- `error`：聚合失败时返回

**示例**：

```go
var total float64
qb.TimeRange(tr).Where("status = ?", 1).Sum("amount", &total)
fmt.Printf("总金额: %.2f\n", total)
```

#### Min

```go
func (qb *QueryBuilder) Min(column string, dest interface{}) error
```

便捷聚合：`SELECT MIN(column) AS <column>_min FROM ...`。分片自动取最小。

**参数**：
- `column` `string`：列名
- `dest` `interface{}`：数值指针

**示例**：

```go
var minPrice float64
qb.TimeRange(tr).Where("status = ?", 1).Min("price", &minPrice)
```

#### Max

```go
func (qb *QueryBuilder) Max(column string, dest interface{}) error
```

便捷聚合：`SELECT MAX(column) AS <column>_max FROM ...`。分片自动取最大。

**参数**：
- `column` `string`：列名
- `dest` `interface{}`：数值指针

**示例**：

```go
var maxID int64
qb.TimeRange(tr).Max("id", &maxID)
```

#### Avg

```go
func (qb *QueryBuilder) Avg(column string, dest interface{}) error
```

便捷聚合。**分片场景下使用全局加权平均 = SUM(column) / SUM(\*)**（比直接 avg(avg1, avg2, avg3) 更精确，后者是错误的，分片行数不同时结果不对）。内部同时跑 `SUM(column)` 和 `COUNT(*)`，最后相除。

**参数**：
- `column` `string`：列名
- `dest` `interface{}`：无 GROUP BY 时是 `*float64` 等单值指针；有 GROUP BY 时是 `*[]T`（每个元素是带 GROUP BY key + avg 字段的 struct）。dest 中的 avg 字段名需包含 `avg` / `Average` / `Mean`（大小写不敏感）

**返回值**：
- `error`：聚合失败时返回

**示例**：

```go
// 无 GROUP BY：单值加权平均
var avgPrice float64
qb.TimeRange(tr).Where("status = ?", 1).Avg("price", &avgPrice)
fmt.Printf("平均价格: %.2f\n", avgPrice)

// 有 GROUP BY：按 status 分组的加权平均
type StatusAvg struct {
    Status  int32   `gorm:"column:status"`
    AvgPrice float64 `gorm:"column:avg_price"` // 字段名包含 "avg"
}
var stats []StatusAvg
qb.Group("status").Avg("price", &stats)
```

### 其他查询方法

#### Exists

```go
func (qb *QueryBuilder) Exists() (bool, error)
```

检查是否有任何分片命中条件。**优化策略**：逐分片 `SELECT 1 LIMIT 1`，找到即返回 `true`——不必扫完全部分片。配合 `ctx.Done()` 可以提前退出。

**返回值**：
- `bool`：是否存在匹配记录
- `error`：查询失败时返回

**示例**：

```go
exists, _ := qb.TimeRange(tr).Where("user_id = ?", 123).Exists()
if exists {
    fmt.Println("该用户在这个时间段有订单")
}
```

#### CountParallel

```go
func (qb *QueryBuilder) CountParallel(concurrency int) (int64, error)
```

并行统计所有分片的总行数。每个分片独立 `COUNT(*)` 后求和。

**参数**：
- `concurrency` `int`：并发数，`<=0` 表示全并发

**返回值**：
- `int64`：所有分片匹配的总行数
- `error`：统计失败时返回

**示例**：

```go
total, _ := qb.TimeRange(tr).Where("status = ?", 1).CountParallel(8)
fmt.Printf("活跃订单总数: %d\n", total)
```

#### Pluck

```go
func (qb *QueryBuilder) Pluck(column string, dest interface{}) error
```

从所有分片表中提取指定列，合并到 dest 切片。**不做去重**，需要去重请自行处理。

**参数**：
- `column` `string`：要提取的列名
- `dest` `interface{}`：`*[]ElementType`（如 `*[]int64` / `*[]string`）

**返回值**：
- `error`：查询失败时返回

**示例**：

```go
var userIDs []int64
qb.Where("status = ?", 1).Pluck("user_id", &userIDs)
// userIDs 是所有分片匹配的 user_id 合集（可能重复）
```

#### Scan

```go
func (qb *QueryBuilder) Scan(dest interface{}) error
```

扫描查询结果到 dest（可以是 struct slice、map slice 等）。自动选择最优路径：

- **单表** → 直接 GORM Scan
- **多表 + 无 GROUP BY/ORDER BY/LIMIT** → 并行各分片 Scan 再合并
- **多表 + 有 GROUP BY/ORDER BY/LIMIT** → UNION ALL 保证排序/分页语义

**参数**：
- `dest` `interface{}`：目标切片指针

**返回值**：
- `error`：查询失败时返回

**示例**：

```go
// 配合 GROUP BY 做跨分片聚合
type Stat struct {
    Status int32 `gorm:"column:status"`
    Cnt    int64 `gorm:"column:cnt"`
}
var stats []Stat
qb.Select("status, COUNT(*) AS cnt").Group("status").Scan(&stats)
```

### 写入 / 更新方法

#### CreateInBatches（分片路由写入）

```go
func (qb *QueryBuilder) CreateInBatches(records interface{}, batchSize int) (int64, error)
```

**分片批量写入核心方法**。按 ShardRule 把 records 中的每条记录自动路由到正确的分片表，然后对每个分片表按 `batchSize` 分批 INSERT。

**路由依赖**：
- **ByYear / ByMonth / ByWeek / ByTenDay**：需要 records 有 `time.Time` 字段（自动识别 `CreatedAt` / `UpdatedAt` / `created_at` / `updated_at`）
- **ByIDHash / ByIDRange**：需要 records 有 `int64` 类型的主键字段（自动识别 `ID` / `Id` / `id`）

> **重要**：调用前 `ShardRule` **必须绑定**（`NewQueryBuilder(db, rule)` 传入非 nil 的 rule）。

**参数**：
- `records` `interface{}`：`*[]T`（指向切片的指针）
- `batchSize` `int`：每批 INSERT 大小（必须 > 0）

**返回值**：
- `int64`：所有分片总共插入的行数
- `error`：路由或写入失败时返回

**示例**：

```go
type Order struct {
    ID        int64     `gorm:"column:id"`
    UserID    int64     `gorm:"column:user_id"`
    Amount    float64   `gorm:"column:amount"`
    CreatedAt time.Time `gorm:"column:created_at"`
}

qb := sharding.NewQueryBuilder(db, sharding.NewShardRule(sharding.ByMonth, "orders"))

// 构造 10000 条订单（每条带 Sonyflake ID + CreatedAt）
var orders []Order
for i := 0; i < 10000; i++ {
    id, _ := sharding.GenerateID()
    orders = append(orders, Order{
        ID:        int64(id),
        UserID:    int64(rand.Intn(1000)),
        Amount:    rand.Float64() * 1000,
        CreatedAt: time.Now().AddDate(0, -rand.Intn(12), 0), // 随机分布在过去一年
    })
}

// 自动按 CreatedAt 路由到 orders_202601, orders_202602, ... 各分片表
affected, err := qb.CreateInBatches(&orders, 500)
fmt.Printf("成功写入 %d 条订单\n", affected)
```

#### Delete

```go
func (qb *QueryBuilder) Delete(model interface{}) (int64, error)
```

并行删除所有匹配分片表的行。**返回所有分片受影响行数之和**。执行硬删除（`DELETE`），软删除请在 GORM model 里声明 `DeletedAt`。

**参数**：
- `model` `interface{}`：可以为 nil（纯条件删除）或传入 GORM model 类型触发软删除

**返回值**：
- `int64`：删除的总行数
- `error`：删除失败时返回

**示例**：

```go
// 跨分片硬删除过期数据
affected, _ := qb.Where("expired_at < ?", time.Now()).Delete(nil)
fmt.Printf("清理了 %d 条过期记录\n", affected)
```

#### Updates

```go
func (qb *QueryBuilder) Updates(values interface{}) (int64, error)
```

并行更新所有匹配分片表的行。`values` 可以是 `map[string]interface{}` 或 GORM model 结构体。**返回所有分片受影响行数之和**。

**参数**：
- `values` `interface{}`：更新值（map 或 struct）

**返回值**：
- `int64`：更新的总行数
- `error`：更新失败时返回

**示例**：

```go
// 把所有 pending 状态的订单批量置为已支付
affected, _ := qb.TimeRange(sharding.TimeRange{
    Start: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
    End:   time.Date(2026, 11, 30, 23, 59, 59, 0, time.UTC),
}).Where("status = ?", 0).
   Updates(map[string]interface{}{"status": 1, "paid_at": time.Now()})
fmt.Printf("更新了 %d 条订单\n", affected)
```

### 辅助方法

#### GetTables

```go
func (qb *QueryBuilder) GetTables() []string
```

返回当前 QueryBuilder 实际命中的分片表列表（拷贝，调用方无法修改内部状态）。

**典型用途**：TimeRange/IDRange 过滤后查看最终命中了哪些分片，用于日志或后续原始 SQL 操作。

**返回值**：
- `[]string`：分片表名列表的拷贝

**示例**：

```go
tables := qb.TimeRange(tr).Where("status = ?", 1).GetTables()
log.Println("命中分片:", tables) // [orders_202610 orders_202611]
```

#### WithTx

```go
func (qb *QueryBuilder) WithTx(tx *gorm.DB) *QueryBuilder
```

将 QueryBuilder 绑定到一个事务 DB。**链式返回新副本**，不修改原对象。调用方负责最终 `Commit` / `Rollback`。

**参数**：
- `tx` `*gorm.DB`：`db.Begin()` 返回的事务句柄

**返回值**：
- `*QueryBuilder`：绑定了事务的新 QueryBuilder

**示例**：

```go
tx := db.Begin()
defer func() {
    if r := recover(); r != nil {
        tx.Rollback()
    }
}()

qbTx := sharding.NewQueryBuilder(tx, rule)

// 事务内跨分片更新
affected, _ := qbTx.TimeRange(tr).Where("status = ?", 0).
    Updates(map[string]interface{}{"status": 2})

if err := tx.Commit(); err != nil {
    log.Fatal("事务提交失败:", err)
}
```

---

## 8. RawBuilder — 原始 SQL 分片路由

`RawBuilder` 封装原始 SQL + `{table}` 占位符的分片路由执行器。通过 `QueryBuilder.Raw(sql, args...)` 创建。

### 占位符机制

SQL 中使用 `{table}` 占位符，执行时会被替换为每个分片表的真实表名。支持在同一条 SQL 中多次出现 `{table}`（如子查询、JOIN）。

### 三种执行模式

| 方法 | 行为 | 适用场景 |
|------|------|---------|
| `ScanRaw` | 并行所有分片执行，合并 Scan 结果到切片 | 复杂跨片查询（多表 JOIN、窗口函数） |
| `FirstRaw` | 逐分片执行，命中即返回 | 按条件查单条记录 |
| `ExecRaw` | 并行所有分片执行写操作，返回总影响行数 | 跨分片 UPDATE/DELETE/INSERT |

### 构造方法

#### QueryBuilder.Raw

```go
func (qb *QueryBuilder) Raw(sql string, args ...interface{}) *RawBuilder
```

创建原始 SQL 分片路由执行器。会先校验 `?` 占位符数量与 `args` 数量匹配（不匹配会 panic）。

**参数**：
- `sql` `string`：原始 SQL，支持 `{table}` 占位符
- `args` `...interface{}`：`?` 占位符参数

**返回值**：
- `*RawBuilder`

**示例**：

```go
qb := sharding.NewQueryBuilder(db, rule).TimeRange(tr)

// 读操作
qb.Raw("SELECT * FROM {table} WHERE status = ? AND amount > ?", 4, 100).ScanRaw(&rows)

// 写操作
qb.Raw("UPDATE {table} SET status = ? WHERE expired_at < NOW()", 0).ExecRaw()

// 复杂子查询（多次 {table}）
qb.Raw(`
    SELECT o.* FROM {table} o
    WHERE o.id IN (
        SELECT order_id FROM {table}_items WHERE user_id = ?
    )
`, userID).ScanRaw(&orders)
```

### ScanRaw

```go
func (rb *RawBuilder) ScanRaw(dest interface{}) error
```

并行执行所有分片的 Raw SQL，合并 Scan 结果到 dest。**合并策略**：并行各分片 Scan 后 append 到同一切片（不做排序/分页）。

**参数**：
- `dest` `interface{}`：`*[]T`（指向切片的指针）

**返回值**：
- `error`：执行失败时返回

**示例**：

```go
var rows []Order
qb.TimeRange(tr).Raw("SELECT * FROM {table} WHERE status = ?", 1).ScanRaw(&rows)
```

### FirstRaw

```go
func (rb *RawBuilder) FirstRaw(dest interface{}) error
```

逐分片执行 Raw SQL，返回第一条命中记录。**命中即返回**——不必扫完全部分片。适合根据条件查单条记录的场景。

**参数**：
- `dest` `interface{}`：目标 struct 或 scan dest

**返回值**：
- `error`：查询失败或未找到时返回

**示例**：

```go
var order Order
qb.TimeRange(tr).Raw("SELECT * FROM {table} WHERE id = ? LIMIT 1", 12345).FirstRaw(&order)
```

### ExecRaw

```go
func (rb *RawBuilder) ExecRaw() (int64, error)
```

并行执行写操作（`UPDATE` / `DELETE` / `INSERT`），汇总所有分片的影响行数。

**返回值**：
- `int64`：所有分片受影响行数之和
- `error`：执行失败时返回

**示例**：

```go
affected, _ := qb.TimeRange(tr).
    Raw("UPDATE {table} SET status = ? WHERE expired_at < NOW()", 0).
    ExecRaw()
fmt.Printf("跨分片更新了 %d 行\n", affected)
```

---

## 9. 各数据库实现对比

### 接口与构造函数对照表

| 数据库 | TableManager 实现 | 构造函数 | 文件 |
|--------|-------------------|----------|------|
| **MySQL** | `MySQLTableManager` | `NewMySQLTableManager(db *gorm.DB) *MySQLTableManager` | `mysql_table_manager.go` |
| **PostgreSQL** | `PostgresTableManager` | `NewPostgresTableManager(db *gorm.DB) *PostgresTableManager` | `postgres_table_manager.go` |
| **ClickHouse** | `ClickHouseTableManager` | `NewClickHouseTableManager(db *gorm.DB) *ClickHouseTableManager` | `clickhouse_table_manager.go` |

### CreateTable 行为差异

| 数据库 | 行为 |
|--------|------|
| **MySQL** | 自动从 DDL 中提取原始表名（`CREATE TABLE xxx`），替换为新表名，并追加 `IF NOT EXISTS`。支持硬编码表名的模板复用 |
| **PostgreSQL** | 直接执行传入的 SQL，调用方需自己写好 `IF NOT EXISTS` 和完整表名 |
| **ClickHouse** | 直接执行传入的 SQL，调用方必须包含 `ENGINE` 和 `ORDER BY` 子句（ClickHouse 强制要求） |

### CreateTableLike 行为差异

| 数据库 | 实现 | 说明 |
|--------|------|------|
| **MySQL** | `CREATE TABLE new LIKE source` | 最简洁的克隆语法，包含索引、约束 |
| **PostgreSQL** | 优先 `CREATE TABLE new (LIKE source INCLUDING ALL)`；不支持时 fallback 到 `CREATE TABLE new AS TABLE source WITH NO DATA` | `INCLUDING ALL` 会把默认值、约束、索引一起拷贝 |
| **ClickHouse** | `CREATE TABLE new AS source` | 克隆结构（含 ENGINE），但不拷贝数据 |

### ListTables 行为差异

| 数据库 | 查询方式 |
|--------|---------|
| **MySQL** | `SHOW TABLES LIKE 'prefix%'` |
| **PostgreSQL** | `SELECT tablename FROM pg_tables WHERE tablename LIKE 'prefix%' AND schemaname = current_schema()` |
| **ClickHouse** | `SELECT name FROM system.tables WHERE database = currentDatabase() AND name LIKE 'prefix%'` |

### MySQL 使用示例

```go
db, _ := gorm.Open(mysql.Open("user:pass@tcp(127.0.0.1:3306)/mydb?charset=utf8mb4"), &gorm.Config{})
mgr := sharding.NewMySQLTableManager(db)
ctx := context.Background()

exists, _ := mgr.TableExists(ctx, "orders_202611")
if !exists {
    mgr.CreateTableLike(ctx, "orders_202611", "orders_template")
}

mgr.CreateTable(ctx, "orders_202612", `
    CREATE TABLE orders (
        id BIGINT UNSIGNED PRIMARY KEY,
        user_id BIGINT NOT NULL,
        amount DECIMAL(10,2),
        created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
        INDEX idx_user (user_id)
    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4
`)

tables, _ := mgr.ListTables(ctx, "orders_")
fmt.Println(tables) // [orders_202609 orders_202610 orders_202611]

mgr.DropTable(ctx, "orders_202401")
```

### PostgreSQL 使用示例

```go
db, _ := gorm.Open(postgres.Open("host=127.0.0.1 port=5432 user=postgres dbname=mydb sslmode=disable"), &gorm.Config{})
mgr := sharding.NewPostgresTableManager(db)
ctx := context.Background()

exists, _ := mgr.TableExists(ctx, "orders_202611")
if !exists {
    mgr.CreateTableLike(ctx, "orders_202611", "orders_template")
}

mgr.CreateTable(ctx, "orders_202612", `
    CREATE TABLE IF NOT EXISTS orders_202612 (
        id BIGSERIAL PRIMARY KEY,
        user_id BIGINT NOT NULL,
        amount NUMERIC(10,2),
        created_at TIMESTAMP DEFAULT NOW()
    )
`)
```

### ClickHouse 使用示例

```go
db, _ := gorm.Open(clickhouse.Open("tcp://127.0.0.1:9000?database=mydb"), &gorm.Config{})
mgr := sharding.NewClickHouseTableManager(db)
ctx := context.Background()

mgr.CreateTable(ctx, "events_202611", `
    CREATE TABLE events_202611 (
        id UInt64,
        user_id UInt64,
        event_type String,
        created_at DateTime
    ) ENGINE = MergeTree()
    ORDER BY (user_id, created_at)
`)

mgr.CreateTableLike(ctx, "events_202612", "events_template")
```

---

## 10. 安全设计

### 表名白名单校验

所有涉及**动态表名**的操作都会经过 `isValidTableName` 校验（定义在 `table_manager.go`），防止表名注入：

```
合法字符：a-z, A-Z, 0-9, _
最大长度：64 字符
```

校验覆盖范围：
- `TableManager.TableExists` / `CreateTable` / `CreateTableLike` / `DropTable` / `ListTables`
- `QueryBuilder.validateTables`（Find / FindParallel / First / Aggregate / CountParallel / Pluck / Delete / Updates / Exists / Scan / FindInBatches / RawBuilder 全部方法）

### 占位符安全

`QueryBuilder.Where` / `OrWhere` / `Having` 和 `QueryBuilder.Raw` 会在调用时校验 `?` 占位符数量与 `args` 数量是否匹配。**不匹配会 panic**——这是刻意设计：链式 Builder 的编程错误在开发时就暴露，避免静默产生错误 SQL。

### 依赖边界

| 包 | gorm.io/gorm | sony/sonyflake |
|----|:-----------:|:--------------:|
| `pkg/sharding` | ✅ 唯一引入 | ✅ 唯一引入 |
| `pkg/migration` | ❌ | ❌ |
| `pkg/driver` | ❌ | ❌ |

---

## 11. 完整端到端示例

下面是一个从零到生产可用的完整示例，覆盖分布式 ID → 分片规则 → 建表 → 定时调度 → 写入 → 查询 → 聚合 的全链路。

```go
package main

import (
    "context"
    "log"
    "time"

    "gorm.io/driver/mysql"
    "gorm.io/gorm"

    "meteor-forge/pkg/sharding"
)

type Order struct {
    ID        int64     `gorm:"column:id;primaryKey"`
    UserID    int64     `gorm:"column:user_id;index"`
    Amount    float64   `gorm:"column:amount"`
    Status    int32     `gorm:"column:status"`
    CreatedAt time.Time `gorm:"column:created_at;index"`
}

func main() {
    // ========== 1. 连接数据库 ==========
    db, err := gorm.Open(mysql.Open("root:root@tcp(127.0.0.1:3306)/shop?charset=utf8mb4&parseTime=True"), &gorm.Config{})
    if err != nil {
        log.Fatal("连接数据库失败:", err)
    }

    // ========== 2. 初始化分布式 ID 生成器 ==========
    // 全集群所有节点必须使用相同 startTime
    if err := sharding.InitSonyflake(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
        log.Fatal("初始化 Sonyflake 失败:", err)
    }

    // ========== 3. 创建分片规则（按月分片） ==========
    rule := sharding.NewShardRule(sharding.ByMonth, "orders")

    // 验证路由：任意时间 → 目标分片表
    table, _ := rule.GetTableNameByTime(time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC))
    log.Println("2026-11-02 的订单 →", table) // orders_202611

    // ========== 4. 初始化表管理器 ==========
    mgr := sharding.NewMySQLTableManager(db)
    stm := sharding.NewShardTableManager(mgr, "orders", "", func(shardName string) string {
        return "orders_" + shardName
    })
    // 加载 CREATE TABLE 模板
    stm.LoadTemplateFromString(`
        CREATE TABLE orders (
            id BIGINT UNSIGNED PRIMARY KEY,
            user_id BIGINT NOT NULL,
            amount DECIMAL(10,2),
            status TINYINT DEFAULT 0,
            created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
            INDEX idx_user_created (user_id, created_at)
        ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4
    `)

    // ========== 5. 启动定时调度器（提前 8 个月建表） ==========
    scheduler := sharding.NewScheduler(stm, rule, 24*time.Hour, 6)
    scheduler.Start()
    defer scheduler.Stop()
    // 启动后立即创建 orders_202611 ~ orders_202706（monthsAhead+2=8 个月）

    // 检查当前有哪些分片表
    current, _ := scheduler.GetCurrentShardTable()
    next, _ := scheduler.GetNextShardTable()
    allTables, _ := scheduler.ListAllShardTables()
    log.Printf("当前分片: %s, 下个分片: %s, 现有 %d 张表", current, next, len(allTables))

    // ========== 6. 写入数据（自动分片路由） ==========
    qb := sharding.NewQueryBuilder(db, rule)

    var orders []Order
    for i := 0; i < 1000; i++ {
        id, _ := sharding.GenerateID()
        orders = append(orders, Order{
            ID:        int64(id),
            UserID:    int64(i % 500),
            Amount:    float64((i % 100) * 9.99),
            Status:    int32(i % 3),
            CreatedAt: time.Now().AddDate(0, -i%12, 0),
        })
    }
    affected, _ := qb.CreateInBatches(&orders, 500)
    log.Printf("写入了 %d 条订单\n", affected)

    // ========== 7. 跨分片查询 ==========
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()

    // 7a: UNION ALL 路径（分片数少，需要 ORDER BY + LIMIT）
    var results []Order
    qb.WithContext(ctx).
        TimeRange(sharding.TimeRange{
            Start: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
            End:   time.Date(2026, 11, 30, 23, 59, 59, 0, time.UTC),
        }).
        Where("status = ?", 1).
        Order("created_at DESC").
        Limit(20).Offset(0).
        Find(&results)
    log.Printf("活跃订单列表: %d 条\n", len(results))

    // 7b: 并行分片路径（分片数多，高并发）
    var bigResults []Order
    qb.Where("user_id = ?", 42).FindParallel(&bigResults, 8)
    log.Printf("用户 42 的订单: %d 条\n", len(bigResults))

    // ========== 8. 跨分片聚合 ==========
    // 总金额（自动跨分片 SUM 合并）
    var total float64
    qb.TimeRange(sharding.TimeRange{
        Start: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
        End:   time.Date(2026, 11, 30, 23, 59, 59, 0, time.UTC),
    }).Where("status = ?", 1).Sum("amount", &total)
    log.Printf("2026 年活跃订单总金额: %.2f\n", total)

    // 统计（并行 COUNT 后求和）
    cnt, _ := qb.TimeRange(sharding.TimeRange{
        Start: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
        End:   time.Date(2026, 11, 30, 23, 59, 59, 0, time.UTC),
    }).Where("status = ?", 1).CountParallel(8)
    log.Printf("2026 年活跃订单总数: %d\n", cnt)

    // ========== 9. 按 ID 路由到单分片 ==========
    // 拿到某条订单 ID，从 ID 里反解出时间 → 知道它在哪个分片表
    sampleID, _ := sharding.GenerateID()
    info, _ := sharding.ParseID(sampleID)
    log.Printf("ID %d 生成于 %v (机器 %d)\n", sampleID, info.Timestamp, info.MachineID)

    // ========== 10. 事务内跨分片更新 ==========
    tx := db.Begin()
    txQB := sharding.NewQueryBuilder(tx, rule)
    txQB.TimeRange(sharding.TimeRange{
        Start: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
        End:   time.Date(2026, 11, 30, 23, 59, 59, 0, time.UTC),
    }).Where("status = ?", 0).Updates(map[string]interface{}{"status": 2})
    if err := tx.Commit(); err != nil {
        tx.Rollback()
        log.Fatal("事务提交失败:", err)
    }
    log.Println("11 月 pending 订单已全部置为已支付")

    // ========== 11. 原始 SQL 复杂查询 ==========
    var joined []Order
    qb.TimeRange(sharding.TimeRange{
        Start: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
        End:   time.Date(2026, 11, 30, 23, 59, 59, 0, time.UTC),
    }).Raw(`
        SELECT o.* FROM {table} o
        WHERE o.amount > ? AND o.status = ?
    `, 100.0, 1).ScanRaw(&joined)
    log.Printf("大额活跃订单: %d 条\n", len(joined))
}
```

---

## 12. 公开 API 速查表

### SonyflakeIDGenerator

| 签名 | 说明 |
|------|------|
| `NewSonyflakeIDGenerator(startTime) (*SonyflakeIDGenerator, error)` | 创建实例 |
| `(*SonyflakeIDGenerator).Generate() (uint64, error)` | 生成唯一 ID |
| `(*SonyflakeIDGenerator).Parse(uint64) (*IDInfo, error)` | 反解 ID |
| `(*SonyflakeIDGenerator).GenerateFromTimestamp(time.Time, uint16) (uint64, error)` | 指定时间合成 ID |

### 包级全局 ID 函数

| 签名 | 说明 |
|------|------|
| `InitSonyflake(time.Time) error` | 初始化全局单例（仅一次） |
| `GenerateID() (uint64, error)` | 生成 ID（便捷） |
| `ParseID(uint64) (*IDInfo, error)` | 反解 ID（便捷） |
| `GenerateIDFromTimestamp(time.Time, uint16) (uint64, error)` | 合成 ID（便捷） |

### ShardRule

| 签名 | 说明 |
|------|------|
| `NewShardRule(ShardType, string, ...Option) *ShardRule` | 创建分片规则 |
| `WithHashShards(int) Option` | 设置哈希分片数（默认 16） |
| `WithRangeSize(int64) Option` | 设置范围分片大小（默认 1,000,000） |
| `(*ShardRule).GetTableNameByTime(time.Time) (string, error)` | 单点时间路由 → 写入 |
| `(*ShardRule).GetTableNameByID(int64) (string, error)` | 单点 ID 路由 → 写入 |
| `(*ShardRule).GetTableNamesByTimeRange(TimeRange) ([]string, error)` | 时间范围枚举 → 跨片查询 |
| `(*ShardRule).GetTableNamesByIDRange(IDRange) ([]string, error)` | ID 范围枚举 → 跨片查询 |

### TableManager 接口

| 签名 | 说明 |
|------|------|
| `TableExists(ctx, table) (bool, error)` | 检查表存在 |
| `CreateTable(ctx, table, sql) error` | 用 DDL 建表 |
| `CreateTableLike(ctx, new, source) error` | 克隆源表结构 |
| `DropTable(ctx, table) error` | 删除表 |
| `ListTables(ctx, prefix) ([]string, error)` | 按前缀枚举所有分片 |

构造函数：`NewMySQLTableManager(db)`, `NewPostgresTableManager(db)`, `NewClickHouseTableManager(db)`

### ShardTableManager

| 签名 | 说明 |
|------|------|
| `NewShardTableManager(TableManager, baseName, schemaTpl, nameFormat) *ShardTableManager` | 创建 Facade |
| `(*ShardTableManager).CreateShardTable(ctx, shardName) error` | 用模板建分片表 |
| `(*ShardTableManager).CreateShardTableLike(ctx, new, source) error` | 克隆分片表 |
| `(*ShardTableManager).DropShardTable(ctx, shardName) error` | 删除分片表 |
| `(*ShardTableManager).TableExists(ctx, shardName) (bool, error)` | 检查分片表存在 |
| `(*ShardTableManager).ListShardTables(ctx) ([]string, error)` | 枚举所有分片表 |
| `(*ShardTableManager).LoadTemplateFromFile(filePath) error` | 从磁盘加载 DDL 模板 |
| `(*ShardTableManager).LoadTemplateFromString(string) error` | 从字符串加载 DDL 模板 |
| `(*ShardTableManager).GetCurrentTemplate() string` | 获取当前模板（调试） |

### Scheduler

| 签名 | 说明 |
|------|------|
| `NewScheduler(*ShardTableManager, *ShardRule, interval, monthsAhead) *Scheduler` | 创建调度器 |
| `(*Scheduler).Start()` | 启动（立即执行一次 + 定时循环） |
| `(*Scheduler).Stop()` | 优雅停止（阻塞等待） |
| `(*Scheduler).GetCurrentShardTable() (string, error)` | 计算"当前"分片表名 |
| `(*Scheduler).GetNextShardTable() (string, error)` | 计算"下一个"分片表名 |
| `(*Scheduler).ListAllShardTables() ([]string, error)` | 列出数据库中**实际存在**的所有分片 |

### QueryBuilder 链式方法

| 方法 | 说明 |
|------|------|
| `WithContext(ctx)` | 设置 context（超时/取消） |
| `Tables(tables...)` | 手动指定分片表 |
| `TimeRange(tr)` | 按时间自动枚举分片表 |
| `IDRange(idr)` | 按 ID 自动枚举分片表 |
| `ExcludeTables(tables...)` | 从查询集中排除分片表 |
| `Select(columns...)` | 指定查询列 |
| `Where(query, args...)` | AND WHERE |
| `OrWhere(query, args...)` | OR WHERE |
| `Order(expr)` | ORDER BY |
| `Group(columns...)` | GROUP BY |
| `Having(expr, args...)` | HAVING |
| `Limit(n)` | 限制返回行数 |
| `Offset(n)` | 偏移量 |

### QueryBuilder 查询执行方法

| 方法 | 签名 | 说明 |
|------|------|------|
| `First` | `(dest) → error` | 查单条（逐分片扫描，命中即返回） |
| `Find` | `(dest) → error` | UNION ALL 查多条 |
| `FindParallel` | `(dest, concurrency) → error` | 并行分片查多条 |
| `FindInBatches` | `(dest, batchSize, callback) → error` | 大结果集分批迭代 |
| `CountParallel` | `(concurrency) → (int64, error)` | 并行统计总数 |
| `Pluck` | `(column, dest) → error` | 提取单列 |
| `Exists` | `() → (bool, error)` | 是否有命中（逐分片，命中即返回） |
| `Scan` | `(dest) → error` | 灵活 Scan（自动选 UNION ALL 或并行） |

### QueryBuilder 聚合方法

| 方法 | 签名 | 说明 |
|------|------|------|
| `Aggregate` | `(dest, aggregateExpr) → error` | 通用聚合（自动应用层合并） |
| `Sum` | `(column, dest) → error` | 便捷 SUM |
| `Min` | `(column, dest) → error` | 便捷 MIN |
| `Max` | `(column, dest) → error` | 便捷 MAX |
| `Avg` | `(column, dest) → error` | 加权全局平均（不是简单平均） |

### QueryBuilder 写入/辅助方法

| 方法 | 签名 | 说明 |
|------|------|------|
| `CreateInBatches` | `(records, batchSize) → (int64, error)` | 分片路由批量写入 |
| `Delete` | `(model) → (int64, error)` | 并行跨分片删除 |
| `Updates` | `(values) → (int64, error)` | 并行跨分片更新 |
| `GetTables` | `() → []string` | 获取当前命中的分片表列表 |
| `WithTx` | `(*gorm.DB) → *QueryBuilder` | 绑定事务（返回新副本） |
| `Raw` | `(sql, args...) → *RawBuilder` | 创建原始 SQL 分片路由执行器 |

### RawBuilder 方法

| 方法 | 签名 | 说明 |
|------|------|------|
| `ScanRaw` | `(dest) → error` | 并行执行 + 合并 Scan 结果 |
| `FirstRaw` | `(dest) → error` | 逐分片执行，命中即返回 |
| `ExecRaw` | `() → (int64, error)` | 并行写操作，返回总影响行数 |

---

## 13. 文件清单

| 文件 | 职责 |
|------|------|
| `pkg/sharding/doc.go` | 包级文档与快速开始示例 |
| `pkg/sharding/sonyflake_id.go` | Sonyflake 分布式 ID 生成/解析（实例 + 全局单例） |
| `pkg/sharding/shard_rule.go` | ShardRule + 7 种 ShardType + 单点路由 / 范围枚举 |
| `pkg/sharding/table_manager.go` | TableManager 接口定义 + isValidTableName 白名单校验 |
| `pkg/sharding/mysql_table_manager.go` | MySQL TableManager 实现 |
| `pkg/sharding/postgres_table_manager.go` | PostgreSQL TableManager 实现 |
| `pkg/sharding/clickhouse_table_manager.go` | ClickHouse TableManager 实现 |
| `pkg/sharding/shard_table_manager.go` | ShardTableManager Facade（分片名 → 完整表名 + 模板管理） |
| `pkg/sharding/scheduler.go` | 定时提前建表后台协程 |
| `pkg/sharding/query_builder.go` | QueryBuilder（UNION ALL + 并行分片 + 聚合 + 写入）+ RawBuilder |
| `pkg/sharding/sonyflake_id_test.go` | Sonyflake ID 测试 |
| `pkg/sharding/shard_rule_test.go` | ShardRule 路由/枚举测试 |
| `pkg/sharding/query_builder_test.go` | QueryBuilder 测试 |
| `docs/sharding.md` | 本接口文档 |