// 多租户迁移完整 Demo：连接真实 MySQL，为 tenant_a / tenant_b 两个租户库
// 演示 状态查看（Status）-> 升级（Up）-> 回滚（Down）的全过程。
//
// 前置准备（只需一次）：
//
//	CREATE DATABASE db_tenant_a;
//	CREATE DATABASE db_tenant_b;
//
// 运行（在本目录下）：
//
//	cd examples/demo && go run main.go
//
// 账号密码可通过环境变量覆盖：MYSQL_DSN="user:pass@tcp(127.0.0.1:3306)/%s?..."
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/yuwen002/meteor-forge/pkg/driver/mysql"
	"github.com/yuwen002/meteor-forge/pkg/migration"
	"github.com/yuwen002/meteor-forge/pkg/tenant"

	_ "github.com/go-sql-driver/mysql" // MySQL 驱动
)

func main() {
	ctx := context.Background()

	// 1. 初始化多租户管理器
	tenantManager := tenant.NewMemoryManager()
	defer tenantManager.Close()

	// 模拟连接两个不同的租户数据库（这里以本地两个不同的库为例：db_tenant_a, db_tenant_b）
	// 注意：脚本含多条语句，DSN 必须带 multiStatements=true；
	// parseTime=true 用于正确扫描 applied_at 的 DATETIME 列。
	dsnTpl := os.Getenv("MYSQL_DSN")
	if dsnTpl == "" {
		dsnTpl = "root:123456@tcp(127.0.0.1:3306)/%s?charset=utf8mb4&parseTime=true&multiStatements=true"
	}

	tenants := []string{"tenant_a", "tenant_b"}
	for _, tID := range tenants {
		dbName := fmt.Sprintf("db_%s", tID)
		db, err := sql.Open("mysql", fmt.Sprintf(dsnTpl, dbName))
		if err != nil {
			log.Fatalf("connect to %s failed: %v", dbName, err)
		}
		if err := db.Ping(); err != nil {
			log.Fatalf("ping %s failed: %v (请确保 MySQL 已启动并创建了该库)", dbName, err)
		}

		// 注册到多租户管理器
		if err := tenantManager.Register(tID, db); err != nil {
			log.Fatalf("register tenant %s failed: %v", tID, err)
		}
	}

	// 2. 初始化本地迁移脚本加载器 (指向 migrations 目录)
	// 这里使用标准 os.DirFS
	scriptFS := os.DirFS("./migrations")
	loader := migration.NewFileSystemLoader(scriptFS, ".")

	// 3. 模拟业务流程：依次为每个租户执行数据库迁移
	for _, tID := range tenants {
		fmt.Printf("\n==================== 正在处理租户: %s ====================\n", tID)

		// A. 从多租户管理器获取当前租户的数据库连接
		db, err := tenantManager.GetDB(tID)
		if err != nil {
			log.Printf("获取租户 %s 数据库连接失败: %v", tID, err)
			continue
		}

		// B. 绑定 MySQL 驱动（每个租户一个 Driver 实例，天然按连接隔离）
		dbDriver := mysql.NewDriver(db, "schema_migrations")

		// C. 组装 Migrator 引擎
		migrator := migration.NewDefaultMigrator(loader, dbDriver)

		// D. 查看当前迁移状态
		printStatus(ctx, migrator)

		// E. 执行 Up 迁移
		fmt.Println("--> 正在执行迁移 Up...")
		if err := migrator.Up(ctx); err != nil {
			if errors.Is(err, migration.ErrNoPendingMigrations) {
				fmt.Println("提示: 没有需要执行的新迁移")
			} else {
				log.Printf("迁移 Up 失败: %v", err)
				continue
			}
		} else {
			fmt.Println("成功: 迁移 Up 完成！")
		}

		// F. 再次查看状态
		printStatus(ctx, migrator)

		// G. 回滚最近一次迁移（Down），演示完整状态机流转
		fmt.Println("--> 正在执行回滚 Down...")
		if err := migrator.Down(ctx); err != nil {
			if errors.Is(err, migration.ErrNoPendingMigrations) {
				fmt.Println("提示: 没有可回滚的迁移")
			} else {
				log.Printf("回滚 Down 失败: %v", err)
				continue
			}
		} else {
			fmt.Println("成功: 回滚 Down 完成！")
		}

		// H. 回滚后最终状态
		printStatus(ctx, migrator)
	}
}

// 辅助函数：打印当前迁移状态
func printStatus(ctx context.Context, migrator migration.Migrator) {
	statuses, err := migrator.Status(ctx)
	if err != nil {
		log.Printf("获取状态失败: %v", err)
		return
	}

	fmt.Println("--- 迁移状态列表 ---")
	for _, s := range statuses {
		appliedStr := "[未执行]"
		if s.IsApplied {
			appliedStr = "[已执行]"
		}
		fmt.Printf("版本: %s | 描述: %s | 状态: %s\n", s.Version, s.Description, appliedStr)
	}
	fmt.Println("--------------------")
}
