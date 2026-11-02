// meteor-forge CLI 入口，基于 spf13/cobra + spf13/viper 提供
// up / down / status / create / tenants-up 子命令，可脱离业务代码独立执行数据库迁移。
//
// 配置加载优先级从高到低：
//
//	显式命令行参数 > 环境变量（METEOR_ 前缀，如 METEOR_DSN）> 配置文件 > SetDefault 默认值
//
// 用法：
//
//	meteor-forge up                      # 自动寻找当前目录的 meteor-forge.yaml，日常只需一个命令
//	meteor-forge up --dry-run            # 演练模式：打印将要执行的 SQL，不修改数据库
//	meteor-forge create add_orders_table # 自动生成带时间戳的 up/down 脚本模板
//	meteor-forge tenants-up              # 按配置文件 tenants 列表并发迁移所有租户库
//	meteor-forge up --dsn="..."          # 临时覆盖配置文件中的连接串
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/yuwen002/meteor-forge/pkg/driver/mysql"
	"github.com/yuwen002/meteor-forge/pkg/migration"

	_ "github.com/go-sql-driver/mysql"
)

var (
	// cfgFile --config 显式指定的配置文件路径（空串表示走默认搜索）
	cfgFile string
	// dryRun up 命令的演练模式开关
	dryRun bool
)

// tenantEntry 对应配置文件中 tenants 列表的一项（tenants-up 命令使用）
type tenantEntry struct {
	Name string `mapstructure:"name"`
	DSN  string `mapstructure:"dsn"`
}

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
			ctx := context.Background()

			// 演练模式：只读预览，不写任何数据（连记录表都不创建）
			if dryRun {
				runDryRun(ctx)
				return
			}

			migrator := initMigrator()
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
	upCmd.Flags().BoolVar(&dryRun, "dry-run", false, "演练模式：只打印将要执行的 SQL，不实际操作数据库")

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

	// 4. create 命令：一键生成带时间戳的迁移脚本模板，告别手动命名
	createCmd := &cobra.Command{
		Use:   "create [description]",
		Short: "自动生成新的 .up.sql 和 .down.sql 迁移模板文件",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			desc := args[0]
			targetDir := viper.GetString("dir")

			// 确保目录存在
			if err := os.MkdirAll(targetDir, 0o755); err != nil {
				log.Fatalf("创建目录失败: %v", err)
			}

			// 生成时间戳版本号格式: YYYYMMDDHHMMSS（与 {version}_{desc}.up.sql 命名规范一致）
			version := time.Now().Format("20060102150405")
			upFile := filepath.Join(targetDir, fmt.Sprintf("%s_%s.up.sql", version, desc))
			downFile := filepath.Join(targetDir, fmt.Sprintf("%s_%s.down.sql", version, desc))

			// 防覆盖：同秒重复执行同名 create 时，宁可报错也不能静默截断用户已编写的 SQL
			for _, f := range []string{upFile, downFile} {
				if _, err := os.Stat(f); err == nil {
					log.Fatalf("错误: 文件已存在，拒绝覆盖: %s", f)
				}
			}

			upTpl := fmt.Sprintf("-- 向上迁移脚本: %s_%s\n-- 在此编写 SQL（多条语句需确保 DSN 已开启 multiStatements=true）\n", version, desc)
			downTpl := fmt.Sprintf("-- 回滚脚本: %s_%s（与 up 文件成对，缺失时该版本将无法回滚）\n-- 在此编写回滚 SQL\n", version, desc)
			if err := os.WriteFile(upFile, []byte(upTpl), 0o644); err != nil {
				log.Fatalf("创建 up 文件失败: %v", err)
			}
			if err := os.WriteFile(downFile, []byte(downTpl), 0o644); err != nil {
				log.Fatalf("创建 down 文件失败: %v", err)
			}

			fmt.Println("成功创建迁移脚本模板:")
			fmt.Printf("   %s\n", upFile)
			fmt.Printf("   %s\n", downFile)
		},
	}

	// 5. tenants-up 命令：按配置文件中的租户列表并发执行 Up 迁移
	tenantsUpCmd := &cobra.Command{
		Use:   "tenants-up",
		Short: "并发为配置文件中定义的所有多租户数据库执行 Up 迁移",
		Run: func(cmd *cobra.Command, args []string) {
			ctx := context.Background()

			var tenants []tenantEntry
			if err := viper.UnmarshalKey("tenants", &tenants); err != nil || len(tenants) == 0 {
				log.Fatal("错误: 未能在配置文件中找到有效的 tenants 配置列表（格式见 meteor-forge.yaml.example）")
			}

			migrationsDir := viper.GetString("dir")
			tableName := viper.GetString("table")

			fmt.Printf("--> 开始并发执行多租户迁移，共计 %d 个租户...\n", len(tenants))

			var wg sync.WaitGroup
			var failed int64
			// 信号量限制并发度：租户数量很大时避免同时打开海量连接压垮数据库
			sem := make(chan struct{}, 8)

			for _, t := range tenants {
				wg.Add(1)
				go func(tenant tenantEntry) {
					defer wg.Done()
					sem <- struct{}{}
					defer func() { <-sem }()

					if err := upgradeTenant(ctx, tenant, migrationsDir, tableName); err != nil {
						atomic.AddInt64(&failed, 1)
						fmt.Printf("✗ [租户 %s] %v\n", tenant.Name, err)
						return
					}
					fmt.Printf("✓ [租户 %s] 迁移完成\n", tenant.Name)
				}(t)
			}
			wg.Wait()

			if n := atomic.LoadInt64(&failed); n > 0 {
				fmt.Fprintf(os.Stderr, "%d/%d 个租户迁移失败\n", n, len(tenants))
				os.Exit(1) // 非零退出码，便于 CI/脚本感知部分失败
			}
			fmt.Println("成功: 所有租户迁移任务执行完毕！")
		},
	}

	// 挂载所有子命令
	rootCmd.AddCommand(upCmd, downCmd, statusCmd, createCmd, tenantsUpCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// upgradeTenant 为单个租户连接数据库并执行 Up，返回可读的错误描述。
func upgradeTenant(ctx context.Context, tenant tenantEntry, migrationsDir, tableName string) error {
	if tenant.Name == "" || tenant.DSN == "" {
		return errors.New("租户配置不完整：name 与 dsn 均为必填")
	}

	db, err := sql.Open("mysql", tenant.DSN)
	if err != nil {
		return fmt.Errorf("连接数据库失败: %w", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		return fmt.Errorf("Ping 数据库失败: %w", err)
	}

	migrator := migration.NewDefaultMigrator(
		migration.NewFileSystemLoader(os.DirFS(migrationsDir), "."),
		mysql.NewDriver(db, tableName),
	)

	err = migrator.Up(ctx)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, migration.ErrNoPendingMigrations):
		fmt.Printf("ℹ  [租户 %s] 已是最新版本\n", tenant.Name)
		return nil
	case errors.Is(err, migration.ErrChecksumMismatch):
		return fmt.Errorf("检测到脚本被篡改，已中止: %w", err)
	default:
		return fmt.Errorf("迁移失败: %w", err)
	}
}

// runDryRun 演练模式：加载本地脚本并读取库中记录，打印将要执行的 SQL 后返回。
// 刻意不调用 Status/Up（它们会 EnsureTable 建表，写入数据库），
// 记录表不存在时仅警告并按"全部未执行"预览，保证全程只读。
func runDryRun(ctx context.Context) {
	dbDsn := viper.GetString("dsn")
	if dbDsn == "" {
		log.Fatal("错误: 数据库 DSN 不能为空，请在 meteor-forge.yaml 中配置 dsn、设置 METEOR_DSN 或通过 --dsn 传入")
	}

	db, err := sql.Open("mysql", dbDsn)
	if err != nil {
		log.Fatalf("连接数据库失败: %v", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		log.Fatalf("Ping 数据库失败: %v", err)
	}

	driver := mysql.NewDriver(db, viper.GetString("table"))
	loader := migration.NewFileSystemLoader(os.DirFS(viper.GetString("dir")), ".")

	scripts, err := loader.LoadScripts(ctx)
	if err != nil {
		log.Fatalf("错误: 加载本地迁移脚本失败: %v", err)
	}

	records, err := driver.GetAppliedVersions(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "警告: 无法读取迁移记录表（可能尚未创建），以下按全部未执行预览: %v\n", err)
		records = nil
	}
	applied := make(map[string]migration.Record, len(records))
	for _, r := range records {
		applied[r.Version] = r
	}

	fmt.Println("[Dry-Run 模式] 只读预览，不会修改数据库")
	var pending []*migration.Script
	tampered := false
	for _, s := range scripts {
		r, ok := applied[s.Version]
		if !ok {
			pending = append(pending, s)
			continue
		}
		// 顺带做防篡改预检，让用户在真正 Up 之前发现问题
		if r.Checksum != "" && r.Checksum != s.Checksum() {
			tampered = true
			fmt.Printf("⚠  [篡改] 版本 %s 的本地内容与已应用存档不一致 (db=%s, local=%s)\n", s.Version, r.Checksum, s.Checksum())
		}
	}

	if tampered {
		log.Fatal("错误: 存在被篡改的已应用脚本，请先人工核对再执行 Up")
	}
	if len(pending) == 0 {
		fmt.Println("提示: 当前已经是最新版本，没有需要执行的新迁移。")
		return
	}

	for _, s := range pending {
		fmt.Printf("\n========== 待执行: %s (%s) ==========\n%s\n", s.Version, s.Description, s.UpSQL)
	}
	fmt.Printf("\n共 %d 个待执行迁移。确认无误后去掉 --dry-run 正式应用。\n", len(pending))
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
