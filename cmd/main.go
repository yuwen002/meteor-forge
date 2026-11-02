// meteor-forge CLI 入口，基于 spf13/cobra 提供 up / down / status 子命令，
// 可脱离业务代码独立执行数据库迁移。
//
// 用法：
//
//	go run ./cmd status --dsn="user:pass@tcp(127.0.0.1:3306)/dbname?charset=utf8mb4&parseTime=true&multiStatements=true"
//	go run ./cmd up
//	go run ./cmd down
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/spf13/cobra"

	"github.com/yuwen002/meteor-forge/pkg/driver/mysql"
	"github.com/yuwen002/meteor-forge/pkg/migration"

	_ "github.com/go-sql-driver/mysql"
)

// 定义全局命令行参数变量
var (
	dsn           string
	migrationsDir string
	tableName     string
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "meteor-forge",
		Short: "meteor-forge 是一个轻量级数据库迁移与多租户管理工具",
	}

	// 绑定全局持久化参数（所有子命令都可以用）。
	// 注意 DSN 参数：multiStatements=true 允许脚本包含多条语句，
	// parseTime=true 用于正确读取 applied_at 时间列。
	rootCmd.PersistentFlags().StringVarP(&dsn, "dsn", "d", "root:123456@tcp(127.0.0.1:3306)/test?charset=utf8mb4&parseTime=true&multiStatements=true", "MySQL 数据库 DSN 连接串")
	rootCmd.PersistentFlags().StringVarP(&migrationsDir, "dir", "D", "./migrations", "迁移 SQL 文件存放目录")
	rootCmd.PersistentFlags().StringVarP(&tableName, "table", "t", "schema_migrations", "迁移历史记录表名")

	// 1. up 命令
	upCmd := &cobra.Command{
		Use:   "up",
		Short: "执行所有未完成的数据库迁移",
		Run: func(cmd *cobra.Command, args []string) {
			migrator := initMigrator()
			ctx := context.Background()

			fmt.Println("--> 正在执行数据库迁移 Up...")
			if err := migrator.Up(ctx); err != nil {
				if errors.Is(err, migration.ErrNoPendingMigrations) {
					fmt.Println("提示: 当前已经是最新版本，没有需要执行的新迁移")
					return
				}
				log.Fatalf("错误: 迁移 Up 失败: %v", err)
			}
			fmt.Println("成功: 所有迁移已顺利执行完毕！")
		},
	}

	// 2. down 命令
	downCmd := &cobra.Command{
		Use:   "down",
		Short: "回滚最近一次执行的迁移",
		Run: func(cmd *cobra.Command, args []string) {
			migrator := initMigrator()
			ctx := context.Background()

			fmt.Println("--> 正在回滚最近一次迁移...")
			if err := migrator.Down(ctx); err != nil {
				if errors.Is(err, migration.ErrNoPendingMigrations) {
					fmt.Println("提示: 没有可回滚的迁移记录")
					return
				}
				log.Fatalf("错误: 回滚失败: %v", err)
			}
			fmt.Println("成功: 最近一次迁移已成功回滚！")
		},
	}

	// 3. status 命令
	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "查看当前数据库的迁移执行状态",
		Run: func(cmd *cobra.Command, args []string) {
			migrator := initMigrator()
			ctx := context.Background()

			statuses, err := migrator.Status(ctx)
			if err != nil {
				log.Fatalf("错误: 获取迁移状态失败: %v", err)
			}

			fmt.Println("================ 数据库迁移状态 ================")
			for _, s := range statuses {
				appliedStr := "\033[31m[未执行]\033[0m" // 红字
				if s.IsApplied {
					appliedStr = "\033[32m[已执行]\033[0m" // 绿字
				}
				fmt.Printf("版本: %s | 描述: %s | 状态: %s\n", s.Version, s.Description, appliedStr)
			}
			fmt.Println("================================================")
		},
	}

	// 将子命令挂载到根命令上
	rootCmd.AddCommand(upCmd, downCmd, statusCmd)

	// 执行 CLI
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// 辅助初始化函数：根据命令行参数初始化 Migrator 实例
func initMigrator() migration.Migrator {
	// 1. 连接数据库
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		log.Fatalf("连接数据库失败: %v", err)
	}
	if err := db.Ping(); err != nil {
		log.Fatalf("Ping 数据库失败: %v", err)
	}

	// 2. 初始化 Driver
	dbDriver := mysql.NewDriver(db, tableName)

	// 3. 初始化 Loader
	scriptFS := os.DirFS(migrationsDir)
	loader := migration.NewFileSystemLoader(scriptFS, ".")

	// 4. 返回状态机引擎
	return migration.NewDefaultMigrator(loader, dbDriver)
}
