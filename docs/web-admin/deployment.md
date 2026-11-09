# Web 管理后台部署与运维手册

## 1. 部署拓扑与安全边界

当前管理服务由 Go API（`cmd/server`）和独立前端工程（`web/admin`）组成。API 默认监听 `127.0.0.1:8081`，并在启动时拒绝非 loopback 地址。该限制是当前安全边界：不要通过公网、共享网络或未经安全设计的反向代理暴露管理 API。

管理元数据库为独立 SQLite 文件；目标数据库由数据源配置指向。生产使用前应明确区分这两类数据库的备份、访问控制和恢复责任。

## 2. 构建

在受控构建环境执行：

```bash
go test ./...
go build -o meteor-forge-server ./cmd/server
go build -o meteor-forge ./cmd
cd web/admin
npm ci
npm run typecheck
npm run build
```

Go API 二进制和 CLI 二进制分别构建。前端产物由 Vite 输出；当前仓库没有声明 Go API 自动托管前端静态资源，因此部署时应按项目实际的静态资源托管方式配置，不能把开发服务器当成生产服务。

## 3. 配置项

| 环境变量 | 必需 | 默认值/说明 |
|---|---:|---|
| `METEOR_ADMIN_ADDR` | 否 | 默认 `127.0.0.1:8081`；仅允许 localhost、127.0.0.1、::1 |
| `METEOR_ADMIN_DB` | 否 | 默认操作系统用户配置目录下的 `meteor-forge/admin.db` |
| `METEOR_ADMIN_ENCRYPTION_KEY` | 是 | 32 字节密钥的 64 位十六进制字符串；使用 `openssl rand -hex 32` 生成 |
| `METEOR_ADMIN_USERNAME` | 首次启动可选 | 初始管理员用户名，默认 `admin`；只在尚无管理员时使用 |
| `METEOR_ADMIN_BOOTSTRAP_PASSWORD` | 首次启动必需 | 首次初始化管理员密码，至少 12 个字符；管理员存在后不会重置密码 |
| `METEOR_MIGRATIONS_DIR` | 否 | 服务端迁移 SQL 目录，默认当前工作目录下的 `migrations` |
| `METEOR_ADMIN_API` | 前端开发可选 | Vite 开发代理目标；默认由前端 Vite 配置决定，通常指向本机管理 API |

CLI 迁移配置使用另一组配置：配置文件 `meteor-forge.yaml`、命令行参数和 `METEOR_DSN`、`METEOR_DIR`、`METEOR_TABLE`、`METEOR_DRIVER` 等环境变量。CLI 的配置优先级为显式命令行参数 > 环境变量 > 配置文件 > 默认值。控制库发现还可使用 `control_dsn` 或 `METEOR_CONTROL_DSN`。不要把 CLI 的目标库 DSN 配置误认为 Web 管理元数据库配置。

## 4. 首次启动流程

Linux/macOS 示例：

```bash
export METEOR_ADMIN_ENCRYPTION_KEY="$(openssl rand -hex 32)"
export METEOR_ADMIN_USERNAME='admin'
read -s -p '初始管理员密码: ' METEOR_ADMIN_BOOTSTRAP_PASSWORD
echo
export METEOR_ADMIN_BOOTSTRAP_PASSWORD
./meteor-forge-server
```

Windows PowerShell 可通过安全的密码管理方式设置相同环境变量；不要将真实密码写入脚本仓库或命令历史。

首次启动时服务会创建管理元数据库并初始化管理员。若数据库中尚无管理员且没有设置 bootstrap 密码，服务会拒绝启动。管理员创建后，bootstrap 环境变量不会覆盖已有账号密码。登录后应尽快按组织密码策略修改初始密码。

管理元数据库初始化会在 SQLite 连接上显式启用外键约束，再执行 Schema 初始化；租户、巡检状态、任务进度、会话等已声明外键关系会在写入时受到约束，定义了级联规则的子记录会按规则处理。每次服务启动还会执行只读的 `PRAGMA foreign_key_check`：若发现旧版本遗留的孤立关联，服务会记录涉及表和数量并继续启动，不会自动删除或改写历史记录。请先备份数据库，再根据关联关系人工核对和修复；不要直接删除审计、任务或租户记录。

## 5. 文件权限与密钥管理

- 管理元数据库文件应限制为服务运行用户可读写；当前备份实现会将备份文件权限设为 `0600`，父目录设为 `0700`（受操作系统权限模型约束）。
- 加密密钥应由独立密钥管理系统或安全配置渠道保存，不能与数据库备份放在同一访问边界内。
- 不要将 `METEOR_ADMIN_ENCRYPTION_KEY`、bootstrap 密码、DSN、Cookie 或 session token 写入 Git、日志、工单或前端构建变量。
- 丢失或更换加密密钥会导致已保存 DSN 密文无法解密。密钥轮换不是简单替换环境变量，必须另行设计并验证迁移方案。
- 管理元数据库备份包含加密 DSN、管理员、租户、任务和审计等敏感控制面数据，应加密存储并限制访问。

## 6. 运行与监控

健康检查：

```bash
curl --fail http://127.0.0.1:8081/api/v1/health
```

该接口只提供最小存活信息，不代表所有目标数据源都可用。管理员可在租户管理页按需触发单租户或批量连接巡检；批量最多 100 个租户、4 个并发、单次 5 秒超时。租户连接巡检支持按后台配置定时执行，并提供巡检历史、健康概览和 24 小时 / 7 天趋势查询；定时巡检默认关闭，首次初始化后可在租户管理页启用。

建议运维侧监控：
- API 进程存活、重启次数和标准错误日志。
- 管理元数据库所在磁盘的可用空间、文件权限和备份结果。
- 目标数据库连接失败、迁移任务失败/中断和分片 DDL 任务失败。
- 管理员账号变更、安全审计事件和密钥轮换操作。

不要把健康检查接口开放为可访问内部配置的诊断端点；也不要依赖转发头推断操作者 IP。

## 7. 备份策略

在线备份：

```bash
./meteor-forge metadata-backup --to /secure-backup/meteor-forge/admin-$(date +%Y%m%d-%H%M%S).db
```

执行时需提供与服务相同的 `METEOR_ADMIN_DB` 和 `METEOR_ADMIN_ENCRYPTION_KEY`。备份采用 SQLite `VACUUM INTO` 一致性快照，执行 `PRAGMA integrity_check` 和 schema migration history 校验，不覆盖已有目标文件。

建议：
1. 在重大配置变更、升级前和既定周期内备份。
2. 每次备份后记录 schema version、时间和校验结果。
3. 备份文件与密钥分开保存，且都应有访问控制。
4. 定期执行恢复演练，不以“备份命令成功”代替恢复验证。

备份只覆盖管理元数据库，不覆盖目标业务数据库。业务数据库必须使用各自的原生备份策略。

## 8. 恢复流程

1. 停止管理 API，确认没有其他进程打开管理元数据库。
2. 备份当前数据库文件及其 `-wal`、`-shm` 文件，保留回退副本。
3. 将备份复制到目标数据库同一文件系统下的临时文件。
4. 使用 SQLite 工具执行 `PRAGMA integrity_check;`，必须返回 `ok`。
5. 设置数据库文件权限为 `0600`，再原子替换管理元数据库文件。
6. 仅在服务停止且旧数据库及 WAL/SHM 文件已保留后，清理与旧数据库关联的伴随文件，防止旧 WAL 被应用到恢复后的数据库。
7. 使用备份时相同的 `METEOR_ADMIN_ENCRYPTION_KEY` 启动服务。
8. 登录后台，核对管理员、数据源、租户、任务和审计记录，并逐一测试关键目标数据源。

完整命令和注意事项见 [备份与恢复说明](backup-restore.md)。

## 9. 升级与回滚

- 升级前先备份管理元数据库和所有涉及的目标数据库。
- 检查本次版本的 schema 迁移、API 变更和前端构建产物。
- 不要在存在 queued/running 迁移或分片 DDL 任务时直接切换版本。
- 升级后检查健康接口、登录、数据源列表、租户列表、任务中心和审计页面。
- 若需回退程序版本，先确认新版本是否已对管理元数据库执行不可逆 schema 变更；程序回滚不等于自动回滚 schema。
- 目标数据库迁移失败或任务中断时，先核对数据库实际结构和迁移历史，不要盲目重复执行或自动回滚。

## 10. 故障排查

| 现象 | 排查方向 |
|---|---|
| 服务启动提示缺少加密密钥 | 设置 64 位十六进制的 `METEOR_ADMIN_ENCRYPTION_KEY` |
| 首次启动提示缺少 bootstrap 密码 | 设置至少 12 位的 `METEOR_ADMIN_BOOTSTRAP_PASSWORD` |
| 服务拒绝绑定地址 | 将 `METEOR_ADMIN_ADDR` 设置为 loopback 地址；当前不支持公网绑定 |
| 登录后频繁 401 | 检查浏览器 Cookie、API 代理是否保持同源、服务端时间和会话是否过期 |
| 接口返回 403 | 核对当前角色与 [权限矩阵](permissions.md) |
| 接口返回 409 | 检查数据源是否被租户/分片规则引用、是否存在活动任务、目标状态是否变化 |
| 迁移任务 interrupted | 服务重启时活动任务会被标记为中断；检查目标库实际迁移状态后再决定后续动作 |
| DSN 无法解密 | 检查是否使用创建数据源时相同的管理元数据库加密密钥 |
| 连接测试失败 | 检查目标库网络、账号权限、驱动类型和 DSN；接口不会回传原始敏感错误 |

## 11. 当前不支持的部署模式

当前版本不提供远程管理 API 的 TLS/可信代理模型、跨实例共享登录限流、定时租户连接巡检、自动故障停用和目标数据库备份编排。需要这些能力时，应作为独立功能设计和安全评审，不要通过放宽 loopback 校验或在外部脚本中猜测 API 行为来替代。
