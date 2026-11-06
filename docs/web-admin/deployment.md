# meteor-forge Web 管理后台部署与运维手册

## 1. 部署边界

当前 Web 管理平台由 Go API 和 Vue 前端源码组成：

- Go 服务入口：cmd/server。
- API 默认监听：127.0.0.1:8081。
- 前端工程：web/admin，使用 Vite 开发服务器代理 /api 请求。
- 管理元数据库：SQLite，默认位于操作系统用户配置目录下的 meteor-forge/admin.db。
- 目标数据库：由管理端登记，和管理元数据库相互独立。

**重要限制：** 当前服务强制 loopback 监听，Go 服务不托管前端静态文件，仓库也没有提供可直接公网部署的反向代理/TLS/远程访问方案。不要通过修改监听地址、反向代理或端口映射绕过这一安全边界。当前交付定位为本机或受控开发环境的管理工作台；远程管理部署需要独立的安全设计和实现。

## 2. 构建

### 2.1 构建 Go 管理 API

~~~sh
go test ./...
go build -o meteor-forge-admin ./cmd/server
~~~

Windows 可使用：

~~~powershell
go test ./...
go build -o meteor-forge-admin.exe ./cmd/server
~~~

### 2.2 构建前端

~~~sh
cd web/admin
npm ci
npm run typecheck
npm run build
~~~

构建产物由 Vite 输出到其配置的目录。Go API 不会自动托管该目录；不要误以为只启动 API 二进制就会提供完整 Web 页面。日常本地联调使用 npm run dev。

## 3. 服务配置

| 环境变量 | 必需性 | 默认值/说明 |
|---|---|---|
| METEOR_ADMIN_ADDR | 可选 | 默认 127.0.0.1:8081；只允许 localhost、127.0.0.1 或 ::1 |
| METEOR_ADMIN_DB | 可选 | 默认操作系统用户配置目录下的 meteor-forge/admin.db |
| METEOR_ADMIN_ENCRYPTION_KEY | 必需 | 64 位十六进制字符串，解码后为 32 字节 |
| METEOR_ADMIN_USERNAME | 可选 | 首次初始化管理员用户名，默认 admin |
| METEOR_ADMIN_BOOTSTRAP_PASSWORD | 首次初始化必需 | 仅在尚无管理员时使用，至少 12 个字符；初始化后不会用于重置密码 |
| METEOR_MIGRATIONS_DIR | 可选 | 迁移脚本目录，默认当前工作目录下的 migrations |

生成加密密钥：

~~~sh
openssl rand -hex 32
~~~

密钥用于解密管理元数据库中保存的 DSN。丢失或更换密钥会导致已存储 DSN 无法解密。密钥不得提交 Git、写入前端环境变量或与数据库备份放在同一公开位置。

## 4. 文件与权限

管理元数据库包含数据源配置密文、租户映射、管理员会话、任务和审计记录。应：

- 限制 admin.db 仅服务运行用户可读写。
- 限制父目录访问权限。
- 将密钥放入独立的密钥管理或受限环境配置。
- 备份时同时确保密钥可恢复，但不要将密钥和数据库文件打包到同一个非加密介质。
- 不要将生产 DSN、管理员密码、会话 Cookie 写入日志或代码仓库。

CLI 备份命令会将备份文件权限设为 0600、父目录设为 0700；具体权限语义受操作系统支持情况影响。

## 5. 启动与停止

Linux/macOS 示例：

~~~sh
export METEOR_ADMIN_DB=/var/lib/meteor-forge/admin.db
export METEOR_ADMIN_ENCRYPTION_KEY='由密钥管理系统注入的64位hex密钥'
export METEOR_ADMIN_USERNAME=admin
export METEOR_ADMIN_BOOTSTRAP_PASSWORD='首次启动时使用的强密码'
./meteor-forge-admin
~~~

首次管理员创建后，后续启动不需要再设置 bootstrap password。服务启动时会初始化/升级管理元数据库 schema，并恢复遗留任务状态。正常停止时服务会处理 HTTP shutdown 并关闭任务管理器和连接池。

健康检查：

~~~sh
curl http://127.0.0.1:8081/api/v1/health
~~~

健康检查只表示管理 API 进程可响应，不代表目标数据源可用。

## 6. 备份与恢复

推荐在服务运行期间执行在线备份：

~~~sh
export METEOR_ADMIN_DB=/var/lib/meteor-forge/admin.db
export METEOR_ADMIN_ENCRYPTION_KEY='与服务相同的64位hex密钥'
./meteor-forge metadata-backup --to /var/backups/meteor-forge/admin-YYYYMMDD-HHMMSS.db
~~~

metadata-backup 是 CLI 子命令，构建 CLI：

~~~sh
go build -o meteor-forge ./cmd
~~~

备份命令使用 SQLite VACUUM INTO 创建一致性快照，执行 integrity_check 和 schema migration history 校验，不覆盖已存在文件。备份仅覆盖 meteor-forge 管理元数据库，不会备份目标 MySQL、PostgreSQL、SQLite 或 ClickHouse 数据库。

恢复操作必须停止管理服务，保留原数据库及 WAL/SHM 文件，校验备份后再替换数据库文件。详细步骤见 backup-restore.md。恢复后必须使用创建备份时相同的加密密钥启动服务。

## 7. 升级流程

1. 阅读本次版本变更和迁移说明，确认管理元数据库 schema 兼容性。
2. 记录当前二进制、配置、迁移脚本版本。
3. 执行管理元数据库在线备份，并确认备份校验成功。
4. 备份加密密钥并验证恢复渠道可用。
5. 停止服务，部署新二进制和前端资源。
6. 启动服务，观察启动日志和健康检查。
7. 登录后台，核对数据源、租户、任务、管理员和审计数据。
8. 对关键目标数据源执行连接测试；确认无误后再恢复日常操作。

如果升级失败，停止继续写入，保留现场和日志，按备份恢复文档回退。不要直接删除旧数据库或 WAL/SHM 文件。

## 8. 任务恢复与排障

- queued/running 迁移任务在服务重启后会标记为 interrupted，不自动重放。
- 任务失败或中断后，先检查目标数据库实际结构和 schema_migrations 记录。
- 取消任务不等于撤销已完成步骤；不要盲目重试。
- 同一数据源的迁移任务串行执行；不同数据源可并发执行。
- 分片 DDL 克隆任务执行有时限和并发限制；取消后也应检查目标表是否已创建。

## 9. 生产化前检查清单

当前仓库尚未提供远程管理部署能力。若计划将来支持远程部署，至少需要先设计并验证：

- TLS 与证书轮换；
- 可信反向代理和客户端来源识别；
- Cookie Secure、SameSite、跨域和 CSRF 策略；
- 网络访问控制、身份接入和管理员 MFA；
- 审计保留、完整性保护和告警；
- 密钥托管、轮换和灾难恢复；
- 多实例任务协调、锁和连接池容量管理；
- 前端静态资源与 API 的受支持部署拓扑。

在这些能力实现并通过安全评审前，必须维持 loopback-only 访问限制。
