// meteor-forge CLI 入口，基于 spf13/cobra 提供 up / down / status 子命令，
// 可脱离业务代码独立执行数据库迁移。
//
// 配置加载基于 spf13/viper，优先级从高到低：
//
//	显式命令行参数 > 环境变量（METEOR_ 前缀，如 METEOR_DSN）> 配置文件 > SetDefault 默认值
//
// 用法：
//
//	meteor-forge up                      # 自动寻找当前目录的 meteor-forge.yaml，日常只需一个命令
//	meteor-forge status                  # 配置文件不存在时也可用，走 flag/默认值
//	meteor-forge up --config=prod.yaml   # 显式指定配置文件路径
//	meteor-forge up --dsn="..."          # 临时覆盖配置文件中的连接串
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/yuwen002/meteor-forge/pkg/driver/mysql"
	"github.com/yuwen002/meteor-forge/pkg/migration"

	_ "github.com/go-sql-driver/mysql"
)

// cfgFile --config 显式指定的配置文件路径（空串表示走默认搜索）
var cfgFile string

func main() {
	rootCmd := &cobra.Command{
		Use:   "meteor-forge",
		Short: "meteor-forge 是一个轻量级数据库迁移与多租户管理工具",
	}

	// 绑定全局持久化参数。
	// 注意：dsn/dir/table 的 flag 默认值必须保持空串——viper.BindPFlag 下
	// flag 的非空默认值会遮蔽配置文件里的值（经典优先级坑），
	// 默认值统一由 viper.SetDefault 提供，各配置层才能按预期生效。
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "配置文件路径 (默认在当前目录寻找 meteor-forge.yaml/.json/.toml)")
	rootCmd.PersistentFlags().StringP("dsn", "d", "", "MySQL 数据库 DSN 连接串")
	rootCmd.PersistentFlags().StringP("dir", "D", "", "迁移 SQL 文件存放目录")
	rootCmd.PersistentFlags().StringP("table", "t", "", "迁移历史记录表名")

	// 将 flag 绑定到 viper：显式传入的 flag 覆盖配置文件与环境变量
	_ = viper.BindPFlag("dsn", rootCmd.PersistentFlags().Lookup("dsn"))
	_ = viper.BindPFlag("dir", rootCmd.PersistentFlags().Lookup("dir"))
	_ = viper.BindPFlag("table", rootCmd.PersistentFlags().Lookup("table"))

	// 在 cobra 命令执行前初始化配置
	cobra.OnInitialize(initConfig)

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
				if errors.Is(err, migration.ErrChecksumMismatch) {
					log.Fatalf("错误: 检测到已应用过的迁移脚本被篡改，为防代码与数据库结构不一致已中止: %v", err)
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

	rootCmd.AddCommand(upCmd, downCmd, statusCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// initConfig 读取配置文件与环境变量。
// 默认搜索路径下找不到配置文件属于正常情况（完全依赖 flag/env 也可用）；
// 用户显式 --config 指定的路径若不存在或解析失败，则直接报错，避免拼写错误被静默吞掉。
func initConfig() {
	if cfgFile != "" {
		// 用户显式指定了配置文件
		viper.SetConfigFile(cfgFile)
	} else {
		// 默认在当前目录寻找 meteor-forge.yaml / .json / .toml（类型按扩展名自动识别）
		viper.AddConfigPath(".")
		viper.SetConfigName("meteor-forge")
	}

	// 环境变量支持：METEOR_DSN / METEOR_DIR / METEOR_TABLE（容器化部署免改文件切库）
	viper.SetEnvPrefix("METEOR")
	viper.AutomaticEnv()

	// 兜底默认值（注意 dsn 故意不设默认：连库地址必须显式给出，防止误连测试库）
	viper.SetDefault("dir", "./migrations")
	viper.SetDefault("table", "schema_migrations")

	if err := viper.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if errors.As(err, &notFound) && cfgFile == "" {
			return
		}
		log.Fatalf("错误: 读取配置文件失败: %v", err)
	}
	// 走 stderr 提示实际生效的配置，不污染 status 等命令的标准输出
	if used := viper.ConfigFileUsed(); used != "" {
		fmt.Fprintf(os.Stderr, "已加载配置文件: %s\n", used)
	}
}

// initMigrator 从 viper 获取最终合并值（flag > env > 配置文件 > 默认值）并组装迁移引擎。
// 注意 DSN 参数：multiStatements=true 允许脚本包含多条语句，
// parseTime=true 用于正确读取 applied_at 时间列。
func initMigrator() migration.Migrator {
	dbDsn := viper.GetString("dsn")
	if dbDsn == "" {
		log.Fatal("错误: 数据库 DSN 不能为空，请在 meteor-forge.yaml 中配置 dsn、设置 METEOR_DSN 或通过 --dsn 传入")
	}

	// 1. 连接数据库
	db, err := sql.Open("mysql", dbDsn)
	if err != nil {
		log.Fatalf("连接数据库失败: %v", err)
	}
	if err := db.Ping(); err != nil {
		log.Fatalf("Ping 数据库失败: %v", err)
	}

	// 2. 初始化 Driver
	dbDriver := mysql.NewDriver(db, viper.GetString("table"))

	// 3. 初始化 Loader
	scriptFS := os.DirFS(viper.GetString("dir"))
	loader := migration.NewFileSystemLoader(scriptFS, ".")

	// 4. 返回状态机引擎
	return migration.NewDefaultMigrator(loader, dbDriver)
}
