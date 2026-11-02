# meteor-forge

> A lightweight, high-performance database schema migration and multi-tenancy routing toolkit for Go.

[![Go Version](https://img.shields.io/badge/Go-%3E%3D%201.25-blue?style=flat-square&logo=go)](https://golang.org/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg?style=flat-square)](https://opensource.org/licenses/MIT)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg?style=flat-square)](https://github.com/yuwen002/meteor-forge/pulls)

**meteor-forge** 是一个专为 Go 后端开发者打造的轻量级数据库迁移（Migration）与多租户（Multi-Tenancy）管理工具。它既可以作为独立的 **CLI 命令行工具** 在运维/CI 流程中使用，也可以作为**轻量级 Go 核心库**无缝嵌入到你的 SaaS 后端服务中。

---

## ✨ 核心特性

* **轻量无负担**：单二进制文件部署（配合 `go:embed` 将迁移 SQL 打进二进制），无沉重依赖，启动极快。
* **多租户友好**：内置多租户连接池管理器（`tenant.Manager`）与动态上下文路由支持，轻松应对 SaaS 系统的分库/多 Schema 隔离场景。
* **契约与解耦（Driver 架构）**：核心迁移引擎 `pkg/migration` 与底层数据库彻底解耦，只面向 `Driver` 接口编程；各数据库适配器位于 `pkg/driver/` 下并依赖核心包，新增驱动无需改动引擎。目前已支持 **MySQL、PostgreSQL、SQLite、ClickHouse** 四大数据库，覆盖从嵌入式到 OLTP/OLAP 的主流场景。
* **现代化 CLI 交互**：基于 `Cobra` + `Viper` 打造，提供直观的 `up`、`down`、`status` 命令与自动补全；支持配置文件（`meteor-forge.yaml`）与环境变量，日常开发只需一个简短命令。
* **防篡改校验（Checksum）**：每次应用迁移时存档脚本内容的 sha256 摘要，Up 前比对已应用脚本的本地内容与库中存档，不一致则中止迁移，避免“代码与数据库对不上”的线上事故（行尾差异已归一化，不会被 git autocrlf 等工具链噪声误报）。
* **事务安全**：PostgreSQL / SQLite 驱动下迁移脚本与版本记录在同一事务中执行，DDL 失败随事务完整回滚；MySQL 驱动同样使用事务（注意其 DDL 隐式提交，原子性覆盖 DML 与记录写入）；ClickHouse 无 ACID 事务，采用先脚本后记录的直行策略。
* **分库分表与分布式 ID（`pkg/sharding`，源自 GoShard 合并）**：Sonyflake 全局唯一 ID 生成/解析，7 种分片策略（年/季/月/周/旬/ID 哈希/ID 范围），分片表自动建表与定时调度，跨分片 UNION ALL / 并行查询构建器（表名白名单防注入）。按子包独立，不需要分片能力的项目可完全无视它。

---

## 📁 项目结构

```text
.
├── cmd/                # CLI 命令行入口 (Cobra 实现)
├── pkg/
│   ├── migration/      # 核心迁移引擎 (types / 接口 / Loader / Migrator 状态机)
│   ├── driver/         # 数据库驱动实现 (mysql / postgres / sqlite / clickhouse)
│   ├── sharding/       # 分库分表：分布式 ID / 分片规则 / 表管理 / 跨片查询（源自 GoShard）
│   └── tenant/         # 多租户连接池与上下文管理器
├── examples/           # 完整的使用示例与测试 SQL
│   ├── basic/          # 零依赖组装演示（go:embed + 内存驱动，可直接运行）
│   ├── demo/           # 真实 MySQL 多租户端到端示例
│   └── sharding/       # 分布式 ID + 分表路由演示（纯逻辑，无需数据库）
├── go.mod
└── README.md
```

## 🚀 快速上手 (CLI 用法)

### 1. 安装

```bash
go build -o meteor-forge ./cmd
# 开发期也可直接运行：go run ./cmd <command>
```

### 2. 准备迁移 SQL 文件

在目录下创建 `migrations/` 文件夹，并按规范命名 SQL 文件（`{版本号}_{描述}.up.sql` / `.down.sql`；up 脚本必须存在，down 缺失时该版本将无法回滚）：

```text
migrations/
├── 20261102001_create_users_table.up.sql
├── 20261102001_create_users_table.down.sql
├── 20261102002_add_email_to_users.up.sql
└── 20261102002_add_email_to_users.down.sql
```

### 3. 配置连接（推荐：配置文件）

复制项目根目录的 `meteor-forge.yaml.example` 为 `meteor-forge.yaml`（已被 `.gitignore` 排除，不会泄漏凭据）：

```yaml
# meteor-forge.yaml
dsn: "root:123456@tcp(127.0.0.1:3306)/test_db?charset=utf8mb4&parseTime=true&multiStatements=true"
dir: "./migrations"
table: "schema_migrations"
```

配置优先级（Viper）：**显式命令行参数 > 环境变量（`METEOR_DSN` / `METEOR_DIR` / `METEOR_TABLE`）> 配置文件 > 内置默认值**。也支持 `meteor-forge.json` / `.toml`，或用 `--config` 指定任意路径（如多环境切换：`--config=prod.yaml`）。配置文件不存在时静默回退到 flag，完全基于参数的旧用法不受影响；但 `--config` 显式指定的路径若不存在会直接报错，避免拼写错误被静默吞掉。

### 4. 使用 CLI 执行管理命令

> **注意**：脚本包含多条语句时，DSN 必须携带 `multiStatements=true`；查询迁移记录需要 `parseTime=true`。

有了配置文件后，日常只需：

```bash
meteor-forge status   # 查看迁移状态
meteor-forge up       # 执行所有未完成的迁移
meteor-forge down     # 回滚最近一次迁移
```

临时切库用 flag 覆盖即可：

```bash
meteor-forge up --dsn="root:123456@tcp(127.0.0.1:3306)/other_db?charset=utf8mb4&parseTime=true&multiStatements=true"
```

常用参数：`-d/--dsn`、`-D/--dir`、`-t/--table`、`--config`。运行 `meteor-forge --help` 查看全部帮助。

### 5. 生产级特性

**create —— 一键生成带时间戳的脚本模板，告别手动命名：**

```bash
meteor-forge create add_orders_table
# → 在配置的 dir 下生成 {YYYYMMDDHHMMSS}_add_orders_table.up.sql / .down.sql
```

**dry-run —— 只读预览，不碰数据库（连记录表都不创建）：**

```bash
meteor-forge up --dry-run
# 列出并打印每个待执行脚本的 UpSQL，同时附带防篡改预检，确认后去掉 --dry-run 正式应用
```

**tenants-up —— 按配置文件中的租户列表并发迁移（信号量限流 + 部分失败非零退出码）：**

```yaml
# meteor-forge.yaml 增加 tenants 段（dir/table 沿用全局设置）
tenants:
  - name: "tenant_alpha"
    dsn: "root:password@tcp(127.0.0.1:3306)/db_tenant_alpha?charset=utf8mb4&parseTime=true&multiStatements=true"
  - name: "tenant_beta"
    dsn: "root:password@tcp(127.0.0.1:3306)/db_tenant_beta?charset=utf8mb4&parseTime=true&multiStatements=true"
```

```bash
meteor-forge tenants-up
# ✓/✗/ℹ 逐租户报告结果；任一租户失败时以非零退出码结束，便于 CI 感知
```

**精细化版本控制 —— 步数回滚与精准跳转：**

```bash
meteor-forge down --steps 3                   # 自最近版本起连续回滚 3 步（不足 3 步则退到空即停）
meteor-forge goto --version 20261102001       # 精准对齐到任意版本：自动判断升级或回滚，多退少补
meteor-forge goto --version 0                 # 保留值 0：一键回滚全部已应用迁移
meteor-forge up --to 20261102001              # 升级到指定版本后停止（goto 的别名入口）
meteor-forge down --to 20261102001            # 回滚至指定版本
```

`down --steps` 全程只加一次迁移锁（批量原子）：多实例环境下不会出现步与步之间被其他实例插队抢锁、插入新迁移的中间态；`goto` 对齐到当前状态时幂等返回，不重复执行。

**分布式迁移锁 —— 多实例并发安全：**

所有写路径（Up / Down / DownSteps / ToVersion）执行前会自动获取数据库级排他锁，
多实例/流水线同时触发同一库迁移时串行互斥，抢锁超时（10s）则中止并报非零退出码：

- MySQL：命名锁 `GET_LOCK`（`RELEASE_LOCK` 归还；进程崩溃断连时服务端自动释放）
- PostgreSQL：会话级 advisory lock（`pg_advisory_lock`，锁键由表名 hashtext 得出）
- ClickHouse：`getLock()` 函数（待真实环境验证）
- SQLite：单写者文件锁天然串行，空操作实现

锁名与记录表名绑定：不同库/不同记录表的迁移互不阻塞；只读操作（status、dry-run）不加锁。

**tenants-sync —— 从控制平面动态发现租户（SaaS 水平扩展免改配置）：**

```yaml
# meteor-forge.yaml 配置控制库 DSN（也可用环境变量 METEOR_CONTROL_DSN）
control_dsn: "root:password@tcp(127.0.0.1:3306)/control_db?charset=utf8mb4&parseTime=true&multiStatements=true"
# 控制库中的发现表：仅 status='active' 的租户会被迁移
# CREATE TABLE t_tenants (tenant_id VARCHAR(64) PRIMARY KEY, dsn VARCHAR(512) NOT NULL, status VARCHAR(16));
```

```bash
meteor-forge tenants-sync
# 启动时从 t_tenants 拉取活跃租户列表，复用并发执行器；新增租户只需插行，无需碰配置文件
```

> **防篡改说明**：版本记录表含 `checksum` 列（存档已应用脚本的 sha256），Up 时若发现已应用过的脚本内容被修改会直接中止。从旧版升级的用户对存量表补一次列即可：`ALTER TABLE schema_migrations ADD COLUMN checksum VARCHAR(64) NOT NULL DEFAULT ''`，存量空哈希记录会自动跳过比对，不会误报。

## 📦 作为 Go 库集成到项目中

如果你希望在自己的 Go 后端服务中动态执行迁移或管理多租户连接，可以这样引入：

```go
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	"github.com/yuwen002/meteor-forge/pkg/driver/mysql"
	"github.com/yuwen002/meteor-forge/pkg/migration"

	_ "github.com/go-sql-driver/mysql"
)

func main() {
	ctx := context.Background()

	// 1. 连接数据库
	db, err := sql.Open("mysql", "root:123456@tcp(127.0.0.1:3306)/test_db?multiStatements=true&parseTime=true")
	if err != nil {
		panic(err)
	}
	defer db.Close()

	// 2. 初始化 MySQL 驱动与本地脚本加载器
	dbDriver := mysql.NewDriver(db, "schema_migrations")
	loader := migration.NewFileSystemLoader(os.DirFS("./migrations"), ".")

	// 3. 创建 Migrator 引擎
	migrator := migration.NewDefaultMigrator(loader, dbDriver)

	// 4. 执行 Up 迁移
	if err := migrator.Up(ctx); err != nil {
		fmt.Printf("Migration failed: %v\n", err)
		return
	}
	fmt.Println("Migration applied successfully!")
}
```

多租户分库场景下，配合 `tenant.MemoryManager` 为每个租户注册独立 `*sql.DB`，逐租户构造 `mysql.NewDriver(db, ...)` 执行迁移——完整流程参见 [examples/demo](examples/demo/main.go)。

精细化版本控制同样提供库调用入口（与 CLI 的 `down --steps` / `goto` 对应）：

```go
// 自最近版本起连续回滚 3 步：全程单次持有迁移锁（批量原子），不足 3 步则退到空即停
err := migrator.DownSteps(ctx, 3)

// 精准对齐到目标版本：自动判断升级或回滚（多退少补），已对齐时幂等返回 ErrNoPendingMigrations
err = migrator.ToVersion(ctx, "20261102002")

// 保留值 "0"：回滚全部已应用迁移（goto --version=0 的引擎侧语义）
err = migrator.ToVersion(ctx, "0")
```

### ClickHouse（OLAP）接入

得益于 Driver 接口设计，切换到 ClickHouse 只需替换驱动实例，核心引擎代码零改动：

```go
import (
	"github.com/yuwen002/meteor-forge/pkg/driver/clickhouse"
	"github.com/yuwen002/meteor-forge/pkg/migration"

	_ "github.com/ClickHouse/clickhouse-go/v2" // 引入 ClickHouse 驱动
)

func initClickHouseMigrator(db *sql.DB) migration.Migrator {
	chDriver := clickhouse.NewDriver(db, "schema_migrations")
	loader := migration.NewFileSystemLoader(os.DirFS("./migrations_ch"), ".")
	return migration.NewDefaultMigrator(loader, chDriver)
}
```

与 MySQL 驱动的两点差异（ClickHouse 架构特性使然）：版本记录表使用 `MergeTree` 引擎并显式声明 `ORDER BY`；无 ACID 事务，回滚通过 `ALTER TABLE ... DELETE`（Mutation）维护记录，生产接入建议会话级设置 `mutations_sync=2` 以避免异步删除导致的短暂状态不一致。

## 🧩 分库分表与分布式 ID（pkg/sharding）

本包由作者的 GoShard 项目合并而来，与迁移引擎互补：分片表的 **schema 版本管理**用 meteor-forge（tenants-up / tenants-sync 逐库逐片升级），**运行时路由与查询**用本包：

```go
// 1. 分布式 ID（Sonyflake：39 位时间 | 16 位机器 | 8 位序列，容器/K8s 环境自动定位 machineID）
gen, _ := sharding.NewSonyflakeIDGenerator(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
id, _ := gen.Generate()
info, _ := gen.Parse(id) // 反解时间戳/机器ID/序列号；支持 GenerateFromTimestamp 回填历史数据

// 2. 分片路由：7 种策略（ByYear/ByQuarter/ByMonth/ByWeek/ByTenDay/ByIDHash/ByIDRange）
rule := sharding.NewShardRule(sharding.ByMonth, "orders")
writeTable := rule.GetTableNameByTime(time.Now()) // orders_202611

// 3. 跨分片查询前的表枚举（与单点路由命名严格一致）
tables, _ := rule.GetTableNamesByTimeRange(sharding.TimeRange{Start: start, End: end})

// 4. 查询构建器：UNION ALL 合并查询 / 并行分片查询 + 应用层归并分页
var dest []Order
err := sharding.NewQueryBuilder(gormDB, rule).
    Tables(tables...).
    Where("status = ?", 4).
    Order("auto_id DESC").Limit(20).
    Find(&dest)
```

**ShardTableManager + TableManager 三实现** —— 按模板自动建分片表（`CREATE TABLE LIKE` 克隆结构），统一接口下提供三种数据库实现：

| 实现 | 建表克隆语法 | 检查表存在 | 表列表查询 |
|------|------------|----------|-----------|
| `MySQLTableManager` | `CREATE TABLE ... LIKE` | `information_schema.tables` + `DATABASE()` | `information_schema.tables` |
| `PostgresTableManager` | `CREATE TABLE ... (LIKE ... INCLUDING ALL)` 主方案，降级 `CREATE TABLE ... AS ... WITH NO DATA` | `information_schema.tables` + `current_schema()` | `information_schema.tables` |
| `ClickHouseTableManager` | `CREATE TABLE new AS source`（ClickHouse AS 只复制结构不带数据） | `system.tables` + `currentDatabase()` | `system.tables` |

调用方注入带正确方言的 `*gorm.DB` 即可，例如 `NewPostgresTableManager(gorm.Open(postgres.Open(dsn)))`。

**Scheduler 定时提前建表** —— 对全部分片类型自动定期检查并创建缺失的未来分片表：

| 分片类型 | 步进周期 | 提前策略 | shardName 格式 |
|---------|---------|---------|---------------|
| `ByYear` | 1 年 | `ceil(monthsAhead/12) + 2` 年 | `"2026"` |
| `ByQuarter` | 3 个月 | `ceil(monthsAhead/3) + 2` 季度 | `"2026Q4"` |
| `ByMonth` | 1 个月 | `monthsAhead + 2` 月 | `"202611"` |
| `ByWeek` | 7 天 | `monthsAhead×5 + 2` 周 | `"2026W44"` |
| `ByTenDay` | 10 天 | `monthsAhead×3 + 2` 段 | `"202611_2"` |
| `ByIDHash` | — | 枚举全部 `HashShards` 个分片 | `"0"` ~ `"15"` |
| `ByIDRange` | — | 不支持（分片取决于运行时业务 ID） | — |

```go
sched := sharding.NewScheduler(shardTableMgr, rule, 1*time.Hour, 6) // 每小时检查一次，提前 6 个月
sched.Start()
defer sched.Stop()
```

> 安全说明：QueryBuilder 的表名经白名单校验（仅字母/数字/下划线，支持别名）后才进入 SQL 拼接；`TableManager` 同样内置表名合法性检查。
> 依赖说明：本包是唯一引入 `gorm.io/gorm`（表管理/查询执行）与 `github.com/sony/sonyflake` 的模块；`pkg/migration` 与 `pkg/driver` 保持零 ORM 依赖不变。
> 连接池建议：本包不引入任何 gorm 方言器（dialector），`*gorm.DB` 由调用方注入；若与迁移引擎共用同一库，建议 `gorm.Open(mysql.New(mysql.Config{Conn: sqlDB}), ...)` 复用已有 `*sql.DB`，避免对同一个 MySQL 开两套连接池。
> 详细设计与 API 参考见 [docs/sharding.md](docs/sharding.md)。

## 🧪 运行示例与测试

```bash
# 单元测试（纯内存，无需任何数据库）
go test ./...

# 零依赖演示：go:embed 加载脚本 + 内存驱动，无需数据库
go run ./examples/basic

# 分库分表演示：分布式 ID + 分表路由 + 跨片表枚举，纯逻辑无需数据库
go run ./examples/sharding

# 端到端演示：需要本地 MySQL，并提前创建两个测试库
#   CREATE DATABASE db_tenant_a; CREATE DATABASE db_tenant_b;
cd examples/demo && go run main.go
```

## 🗺️ Roadmap

- [x] 基础迁移引擎（Up / Down / ToVersion / Status）与核心层单元测试（fstest + MockDriver，覆盖加载/状态机/版本跳转）
- [x] MySQL 驱动适配与事务保障
- [x] PostgreSQL / SQLite 驱动适配
- [x] ClickHouse 驱动适配（MergeTree + Mutation 记录维护，待真实环境验证）
- [x] 基础多租户连接池管理器 (tenant.Manager)
- [x] 校验和防篡改机制（sha256 存档 + 行尾归一化 + Up 前置比对）
- [x] CLI 配置文件与环境变量支持（Viper：flag > env > 配置文件 > 默认值）
- [x] 多租户批量并发迁移命令（tenants-up：协程并发 + 信号量限流 + 非零退出码）
- [x] 生产级 CLI 体验（create 模板生成 / up --dry-run 只读演练 / 配置文件与环境变量）
- [x] 分布式迁移锁（MySQL GET_LOCK / PG advisory lock / CK getLock，多实例并发互斥，崩溃自动释放）
- [x] 精细化版本控制（up --to / down --steps 单锁批量原子退档 / goto --version 双向精准对齐，0 = 回滚全部）
- [x] 动态租户发现（tenants-sync：Control DB + t_tenants 表，SaaS 扩容免改配置）
- [x] 分库分表与分布式 ID（合并 GoShard：Sonyflake / 7 种分片策略 / 跨片查询 / 定时建表，并修复其位段解析、周/季度表名一致性、范围枚举漏表等 5 处 bug）
- [ ] CLI 支持 --driver 参数切换数据库类型（目前 CLI 面向 MySQL）
- [ ] CI 流水线与覆盖率徽章

## 🤝 贡献指南

欢迎提交 Pull Requests 或开启 Issues！如果你有任何想法或建议，随时欢迎交流。

1. Fork 本仓库 (https://github.com/yuwen002/meteor-forge/fork)
2. 创建你的特性分支 (`git checkout -b feature/AmazingFeature`)
3. 提交你的修改 (`git commit -m 'Add some AmazingFeature'`)
4. 推送到分支 (`git push origin feature/AmazingFeature`)
5. 开启一个 Pull Request

## 📄 License

本项目基于 [MIT License](LICENSE) 开源，你可以自由地将其用于商业或个人项目。