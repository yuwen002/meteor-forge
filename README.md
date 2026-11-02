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
* **契约与解耦（Driver 架构）**：核心迁移引擎 `pkg/migration` 与底层数据库彻底解耦，只面向 `Driver` 接口编程；各数据库适配器位于 `pkg/driver/` 下并依赖核心包，新增 PostgreSQL 等驱动无需改动引擎。目前已支持 MySQL（OLTP）与 ClickHouse（OLAP）双引擎。
* **现代化 CLI 交互**：基于 `Cobra` 打造，提供直观的 `up`、`down`、`status` 命令与自动补全。
* **事务安全**：MySQL 驱动下迁移脚本与版本记录写入在同一事务中执行，失败自动回滚，确保变更的原子性（ClickHouse 无行级事务，采用先脚本后记录的补偿策略，详见驱动文档注释）。

---

## 📁 项目结构

```text
.
├── cmd/                # CLI 命令行入口 (Cobra 实现)
├── pkg/
│   ├── migration/      # 核心迁移引擎 (types / 接口 / Loader / Migrator 状态机)
│   ├── driver/         # 数据库驱动实现 (mysql / clickhouse)
│   └── tenant/         # 多租户连接池与上下文管理器
├── examples/           # 完整的使用示例与测试 SQL
│   ├── basic/          # 零依赖组装演示（go:embed + 内存驱动，可直接运行）
│   └── demo/           # 真实 MySQL 多租户端到端示例
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

### 3. 使用 CLI 执行管理命令

> **注意**：脚本包含多条语句时，DSN 必须携带 `multiStatements=true`；查询迁移记录需要 `parseTime=true`。

查看当前迁移状态：

```bash
meteor-forge status --dsn="root:123456@tcp(127.0.0.1:3306)/test_db?charset=utf8mb4&parseTime=true&multiStatements=true" --dir="./migrations"
```

执行升级 (Up - 运行所有未完成的迁移)：

```bash
meteor-forge up --dsn="root:123456@tcp(127.0.0.1:3306)/test_db?multiStatements=true&parseTime=true"
```

执行回滚 (Down - 回滚最近一次迁移)：

```bash
meteor-forge down --dsn="root:123456@tcp(127.0.0.1:3306)/test_db?multiStatements=true&parseTime=true"
```

常用参数：`-d/--dsn`、`-D/--dir`、`-t/--table`（版本记录表名，默认 `schema_migrations`）。运行 `meteor-forge --help` 查看全部帮助。

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

## 🧪 运行示例

```bash
# 零依赖演示：go:embed 加载脚本 + 内存驱动，无需数据库
go run ./examples/basic

# 端到端演示：需要本地 MySQL，并提前创建两个测试库
#   CREATE DATABASE db_tenant_a; CREATE DATABASE db_tenant_b;
cd examples/demo && go run main.go
```

## 🗺️ Roadmap

- [x] 基础迁移引擎（Up / Down / ToVersion / Status）
- [x] MySQL 驱动适配与事务保障
- [x] ClickHouse 驱动适配（MergeTree + Mutation 记录维护，待真实环境验证）
- [x] 基础多租户连接池管理器 (tenant.Manager)
- [ ] 多租户批量自动化并发迁移命令（CLI 遍历租户 + 分布式锁）
- [ ] PostgreSQL 驱动适配
- [ ] 单元测试与 CI 覆盖率徽章

## 🤝 贡献指南

欢迎提交 Pull Requests 或开启 Issues！如果你有任何想法或建议，随时欢迎交流。

1. Fork 本仓库 (https://github.com/yuwen002/meteor-forge/fork)
2. 创建你的特性分支 (`git checkout -b feature/AmazingFeature`)
3. 提交你的修改 (`git commit -m 'Add some AmazingFeature'`)
4. 推送到分支 (`git push origin feature/AmazingFeature`)
5. 开启一个 Pull Request

## 📄 License

本项目基于 [MIT License](LICENSE) 开源，你可以自由地将其用于商业或个人项目。
