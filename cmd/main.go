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
//	meteor-forge goto --version=0        # 精准对齐到任意版本；保留值 0 = 回滚全部迁移
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

	"github.com/yuwen002/meteor-forge/pkg/driver/clickhouse"
	"github.com/yuwen002/meteor-forge/pkg/driver/mysql"
	"github.com/yuwen002/meteor-forge/pkg/driver/postgres"
	"github.com/yuwen002/meteor-forge/pkg/driver/sqlite"
	"github.com/yuwen002/meteor-forge/pkg/migration"
	"github.com/yuwen002/meteor-forge/pkg/tenant"

	_ "github.com/ClickHouse/clickhouse-go/v2"
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

var (
	cfgFile     string
	dryRun      bool
	upTo        string
	downTo      string
	downSteps   int
	gotoVersion string
	dbDriver    string
)

const (
	driverMySQL      = "mysql"
	driverPostgres   = "postgres"
	driverSQLite     = "sqlite"
	driverClickHouse = "clickhouse"
)

// tenantEntry 对应配置文件中 tenants 列表的一项（tenants-up 命令使用）
type tenantEntry struct {
	Name   string `mapstructure:"name"`
	DSN    string `mapstructure:"dsn"`
	Driver string `mapstructure:"driver"`
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
	rootCmd.PersistentFlags().StringP("dsn", "d", "", "数据库 DSN 连接串")
	rootCmd.PersistentFlags().StringP("dir", "D", "", "迁移 SQL 文件存放目录")
	rootCmd.PersistentFlags().StringP("table", "t", "", "迁移历史记录表名")
	rootCmd.PersistentFlags().StringVar(&dbDriver, "driver", "", "数据库类型: mysql | postgres | sqlite | clickhouse")

	_ = viper.BindPFlag("dsn", rootCmd.PersistentFlags().Lookup("dsn"))
	_ = viper.BindPFlag("dir", rootCmd.PersistentFlags().Lookup("dir"))
	_ = viper.BindPFlag("table", rootCmd.PersistentFlags().Lookup("table"))
	_ = viper.BindPFlag("driver", rootCmd.PersistentFlags().Lookup("driver"))

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

			// --to 指定目标版本：双向跳转到精确版本
			var err error
			if upTo != "" {
				err = migrator.ToVersion(ctx, upTo)
			} else {
				err = migrator.Up(ctx)
			}
			if err != nil {
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
	upCmd.Flags().StringVar(&upTo, "to", "", "升级到指定版本后停止（等价于 goto）")

	// 2. down 命令
	downCmd := &cobra.Command{
		Use:   "down",
		Short: "回滚迁移：默认回滚最近一次；--steps 连续回滚多步；--to 回滚至指定版本",
		Run: func(cmd *cobra.Command, args []string) {
			if downSteps > 0 && downTo != "" {
				log.Fatal("错误: --steps 与 --to 不能同时指定")
			}

			migrator := initMigrator()
			ctx := context.Background()

			var err error
			switch {
			case downSteps > 0:
				fmt.Printf("--> 正在连续回滚 %d 步迁移...\n", downSteps)
				err = migrator.DownSteps(ctx, downSteps)
			case downTo != "":
				fmt.Printf("--> 正在回滚至指定版本 %s...\n", downTo)
				err = migrator.ToVersion(ctx, downTo)
			default:
				fmt.Println("--> 正在回滚最近一次迁移...")
				err = migrator.Down(ctx)
			}
			if err != nil {
				if errors.Is(err, migration.ErrNoPendingMigrations) {
					fmt.Println("提示: 没有可回滚的迁移记录")
					return
				}
				log.Fatalf("错误: 回滚失败: %v", err)
			}
			fmt.Println("成功: 回滚已完成！")
		},
	}
	downCmd.Flags().IntVar(&downSteps, "steps", 0, "连续回滚的步数")
	downCmd.Flags().StringVar(&downTo, "to", "", "回滚至指定版本（不含）以下，双向跳转")

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
			var tenants []tenantEntry
			if err := viper.UnmarshalKey("tenants", &tenants); err != nil || len(tenants) == 0 {
				log.Fatal("错误: 未能在配置文件中找到有效的 tenants 配置列表（格式见 meteor-forge.yaml.example）")
			}

			list := make([]tenant.TenantInfo, 0, len(tenants))
			for _, t := range tenants {
				list = append(list, tenant.TenantInfo{ID: t.Name, DSN: t.DSN, Driver: t.Driver})
			}
			migrateTenants(context.Background(), list)
		},
	}

	// 6. tenants-sync 命令：从 Control DB 动态发现活跃租户后并发迁移（SaaS 水平扩展免改配置）
	tenantsSyncCmd := &cobra.Command{
		Use:   "tenants-sync",
		Short: "从主控数据库动态发现活跃租户并并发执行 Up 迁移",
		Run: func(cmd *cobra.Command, args []string) {
			controlDsn := viper.GetString("control_dsn")
			if controlDsn == "" {
				log.Fatal("错误: 控制库 DSN 未配置，请在 meteor-forge.yaml 设置 control_dsn 或通过 METEOR_CONTROL_DSN 环境变量传入")
			}

			controlDB, err := sql.Open("mysql", controlDsn)
			if err != nil {
				log.Fatalf("连接控制库失败: %v", err)
			}
			defer controlDB.Close()
			if err := controlDB.Ping(); err != nil {
				log.Fatalf("Ping 控制库失败: %v", err)
			}

			ctx := context.Background()
			tenants, err := tenant.LoadActiveTenants(ctx, controlDB)
			if err != nil {
				log.Fatalf("错误: 动态发现租户失败: %v", err)
			}
			if len(tenants) == 0 {
				fmt.Println("提示: 控制库中没有发现任何活跃租户")
				return
			}
			migrateTenants(ctx, tenants)
		},
	}

	// 7. goto 命令：精准对齐到目标版本（双向跳转，多退少补；版本 0 回滚全部）
	gotoCmd := &cobra.Command{
		Use:   "goto",
		Short: "将数据库迁移状态精准对齐到目标版本（自动判断升级或回滚；--version=0 回滚全部）",
		Run: func(cmd *cobra.Command, args []string) {
			if gotoVersion == "" {
				log.Fatal("错误: 必须通过 --version 指定目标版本号（或输入 0 回滚全部迁移）")
			}

			migrator := initMigrator()
			ctx := context.Background()

			fmt.Printf("--> 正在将数据库迁移状态精准对齐到版本: %s ...\n", gotoVersion)
			if err := migrator.ToVersion(ctx, gotoVersion); err != nil {
				if errors.Is(err, migration.ErrNoPendingMigrations) {
					fmt.Println("提示: 当前已对齐到目标版本，无需任何操作")
					return
				}
				if errors.Is(err, migration.ErrInvalidVersion) {
					log.Fatalf("错误: 目标版本不存在或非法: %v", err)
				}
				log.Fatalf("错误: 目标版本对齐失败: %v", err)
			}
			fmt.Printf("成功: 数据库状态已对齐到版本 %s！\n", gotoVersion)
		},
	}
	gotoCmd.Flags().StringVarP(&gotoVersion, "version", "v", "", "目标版本号（如 20261102143000；输入 0 回滚全部迁移）")

	// 挂载所有子命令
	rootCmd.AddCommand(upCmd, downCmd, statusCmd, createCmd, tenantsUpCmd, tenantsSyncCmd, gotoCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// errAlreadyUpToDate 内部哨兵：租户库已是最新版本，不算失败也不算新迁移。
var errAlreadyUpToDate = errors.New("already up to date")

// migrateTenants 并发对租户列表执行 Up：信号量限流 + 逐租户报告 + 部分失败非零退出码。
// tenants-up（静态配置）与 tenants-sync（动态发现）共用此执行器。
func migrateTenants(ctx context.Context, tenants []tenant.TenantInfo) {
	migrationsDir := viper.GetString("dir")
	tableName := viper.GetString("table")

	fmt.Printf("--> 开始并发执行多租户迁移，共计 %d 个租户...\n", len(tenants))

	var wg sync.WaitGroup
	var failed int64
	// 信号量限制并发度：租户数量很大时避免同时打开海量连接压垮数据库
	sem := make(chan struct{}, 8)

	for _, t := range tenants {
		wg.Add(1)
		go func(ti tenant.TenantInfo) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			if err := upgradeTenant(ctx, ti, migrationsDir, tableName); err != nil {
				if errors.Is(err, errAlreadyUpToDate) {
					fmt.Printf("ℹ  [租户 %s] 已是最新版本\n", ti.ID)
					return
				}
				atomic.AddInt64(&failed, 1)
				fmt.Printf("✗ [租户 %s] %v\n", ti.ID, err)
				return
			}
			fmt.Printf("✓ [租户 %s] 迁移完成\n", ti.ID)
		}(t)
	}
	wg.Wait()

	if n := atomic.LoadInt64(&failed); n > 0 {
		fmt.Fprintf(os.Stderr, "%d/%d 个租户迁移失败\n", n, len(tenants))
		os.Exit(1) // 非零退出码，便于 CI/脚本感知部分失败
	}
	fmt.Println("成功: 所有租户迁移任务执行完毕！")
}

// upgradeTenant 为单个租户连接数据库并执行 Up，返回可读的错误描述。
// 多实例同时对同一租户库执行时，由驱动层迁移锁保证串行（后来者等待或报锁冲突）。
func upgradeTenant(ctx context.Context, ti tenant.TenantInfo, migrationsDir, tableName string) error {
	if ti.ID == "" || ti.DSN == "" {
		return errors.New("租户配置不完整：id 与 dsn 均为必填")
	}

	drvType := ti.Driver
	if drvType == "" {
		drvType = resolveDriverType()
	}

	db, err := sql.Open(sqlDriverName(drvType), ti.DSN)
	if err != nil {
		return fmt.Errorf("连接数据库失败: %w", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		return fmt.Errorf("Ping 数据库失败: %w", err)
	}

	migrator := migration.NewDefaultMigrator(
		migration.NewFileSystemLoader(os.DirFS(migrationsDir), "."),
		newMigrationDriver(drvType, db, tableName),
	)

	err = migrator.Up(ctx)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, migration.ErrNoPendingMigrations):
		return errAlreadyUpToDate
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

	drvType := resolveDriverType()

	db, err := sql.Open(sqlDriverName(drvType), dbDsn)
	if err != nil {
		log.Fatalf("连接数据库失败: %v", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		log.Fatalf("Ping 数据库失败: %v", err)
	}

	driver := newMigrationDriver(drvType, db, viper.GetString("table"))
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
	viper.SetDefault("driver", driverMySQL)

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

// resolveDriverType 从 viper 读取并校验 driver 类型，非法值直接退出。
func resolveDriverType() string {
	d := viper.GetString("driver")
	switch d {
	case driverMySQL, driverPostgres, driverSQLite, driverClickHouse:
		return d
	case "":
		return driverMySQL
	default:
		log.Fatalf("错误: 不支持的 driver 类型 %q，可选值: mysql | postgres | sqlite | clickhouse", d)
		return ""
	}
}

// sqlDriverName 将内部驱动类型映射到 database/sql 的注册名。
func sqlDriverName(driverType string) string {
	switch driverType {
	case driverMySQL:
		return "mysql"
	case driverPostgres:
		return "pgx"
	case driverSQLite:
		return "sqlite"
	case driverClickHouse:
		return "clickhouse"
	default:
		return "mysql"
	}
}

// newMigrationDriver 根据驱动类型构造对应的 migration.Driver 实例。
func newMigrationDriver(driverType string, db *sql.DB, tableName string) migration.Driver {
	switch driverType {
	case driverMySQL:
		return mysql.NewDriver(db, tableName)
	case driverPostgres:
		return postgres.NewDriver(db, tableName)
	case driverSQLite:
		return sqlite.NewDriver(db, tableName)
	case driverClickHouse:
		return clickhouse.NewDriver(db, tableName)
	default:
		return mysql.NewDriver(db, tableName)
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

	drvType := resolveDriverType()

	db, err := sql.Open(sqlDriverName(drvType), dbDsn)
	if err != nil {
		log.Fatalf("连接数据库失败: %v", err)
	}
	if err := db.Ping(); err != nil {
		log.Fatalf("Ping 数据库失败: %v", err)
	}

	dbDriver := newMigrationDriver(drvType, db, viper.GetString("table"))

	scriptFS := os.DirFS(viper.GetString("dir"))
	loader := migration.NewFileSystemLoader(scriptFS, ".")

	return migration.NewDefaultMigrator(loader, dbDriver)
}