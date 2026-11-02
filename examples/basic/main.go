// 完整使用示例：展示 tenant 与 migration 两个模块的组装方式。
//
// 运行：go run ./examples/basic
//
// 组件分工：
//   - tenant.Manager：管理租户 -> 数据库连接池的路由（本示例用占位 *sql.DB 演示注册流程）
//   - tenant.Resolver / Context：在调用链中传递 tenant_id
//   - migration.FileSystemLoader：通过 go:embed 加载迁移脚本（单二进制随处运行）
//   - migration.DefaultMigrator：版本控制引擎
//   - migration.MemoryDriver：内存占位驱动；生产环境替换为 MySQL/ClickHouse 的 Driver 实现
package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"embed"
	"errors"
	"fmt"
	"log"
	"sync"

	"github.com/yuwen002/meteor-forge/pkg/migration"
	"github.com/yuwen002/meteor-forge/pkg/tenant"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

func main() {
	ctx := context.Background()

	// 1. 租户连接路由：真实场景中通过 sql.Open 为每个租户建立连接池并注册。
	//    这里用占位 *sql.DB 仅演示注册流程，不会真正连接数据库。
	manager := tenant.NewMemoryManager()
	for _, id := range []string{"tenant_alpha", "tenant_beta"} {
		if err := manager.Register(id, placeholderDB()); err != nil {
			log.Fatalf("注册租户 %s 失败: %v", id, err)
		}
	}
	defer manager.Close()

	// 2. 将目标租户写入 Context，Driver 实现可通过 tenant.FromContext 取出并调用 manager.GetDB 路由连接。
	tenantID := "tenant_alpha"
	ctx = tenant.WithTenantID(ctx, tenantID)

	// 也可通过 Resolver 从 Context 解析（模拟中间件行为）：
	resolved, err := tenant.HeaderResolver{}.Resolve(ctx)
	if err != nil {
		log.Fatalf("解析租户失败: %v", err)
	}
	fmt.Printf("Resolver 解析到租户: %s\n", resolved)

	// 3. 组装迁移引擎：embed.FS -> FileSystemLoader -> DefaultMigrator <- MemoryDriver。
	//    生产中把 MemoryDriver 换成独立驱动包即可，组合方式（需真实数据库连接）：
	//    dbDriver := mysql.NewDriver(dbConn, "schema_migrations") // pkg/driver/mysql
	//    migrator := migration.NewDefaultMigrator(loader, dbDriver)
	loader := migration.NewFileSystemLoader(migrationsFS, "migrations")
	driver := migration.NewMemoryDriver()
	migrator := migration.NewDefaultMigrator(loader, driver)

	// 4. 执行所有未完成的迁移。
	if err := migrator.Up(ctx); err != nil {
		log.Fatalf("Up 执行失败: %v", err)
	}
	dumpStatus(ctx, migrator, "Up 之后")

	// 再次 Up：已是最新，引擎返回 ErrNoPendingMigrations，属正常语义。
	if err := migrator.Up(ctx); !errors.Is(err, migration.ErrNoPendingMigrations) {
		log.Fatalf("重复 Up 预期 ErrNoPendingMigrations，实际: %v", err)
	}
	fmt.Println("重复 Up 返回 ErrNoPendingMigrations，符合预期")

	// 5. 回滚最近一次迁移。
	if err := migrator.Down(ctx); err != nil {
		log.Fatalf("Down 执行失败: %v", err)
	}
	dumpStatus(ctx, migrator, "Down 之后")

	// 6. 迁移到指定版本。
	if err := migrator.ToVersion(ctx, "000002"); err != nil {
		log.Fatalf("ToVersion 执行失败: %v", err)
	}
	dumpStatus(ctx, migrator, "ToVersion(000002) 之后")
}

// dumpStatus 打印当前迁移状态。
func dumpStatus(ctx context.Context, m migration.Migrator, label string) {
	items, err := m.Status(ctx)
	if err != nil {
		log.Fatalf("查询状态失败: %v", err)
	}
	applied := 0
	fmt.Printf("== %s ==\n", label)
	for _, s := range items {
		flag := "[ ]"
		if s.IsApplied {
			flag = "[x]"
			applied++
		}
		fmt.Printf("  %s %-10s %s\n", flag, s.Version, s.Description)
	}
	fmt.Printf("  已应用 %d/%d 条迁移\n", applied, len(items))
}

// placeholderDB 创建一个不会真正连接的占位 *sql.DB，仅演示 Manager 注册流程。
// 真实代码应改为：db, err := sql.Open("mysql", dsn)。
var placeholderDB = sync.OnceValue(func() *sql.DB {
	sql.Register("meteor-forge-placeholder", nopSQLDriver{})
	db, err := sql.Open("meteor-forge-placeholder", "placeholder")
	if err != nil {
		log.Fatalf("创建占位连接失败: %v", err)
	}
	return db
})

// nopSQLDriver 空操作 database/sql 驱动，从不实际拨号（sql.Open 是懒连接的）。
type nopSQLDriver struct{}

func (nopSQLDriver) Open(name string) (driver.Conn, error) {
	return nil, errors.New("examples: 占位驱动不支持真实连接")
}
