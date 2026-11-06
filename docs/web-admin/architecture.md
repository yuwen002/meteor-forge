# Web 管理后台架构与开发计划

## 目标

在不破坏现有 Go 核心库和 CLI 的前提下，为 meteor-forge 增加 Web 管理后台，统一管理数据库迁移、多租户和分库分表。

## 总体分层

- `pkg/migration`、`pkg/tenant`、`pkg/sharding`：保持为独立核心能力，不依赖 HTTP、前端或管理元数据库。
- `internal/adminapi`：HTTP API、请求/响应模型和管理用例的适配层。
- `internal/adminstore`：独立 SQLite 管理元数据库；数据源 DSN 使用 AES-256-GCM 加密，API 只返回脱敏元信息。
- `cmd/server`：管理服务启动入口；与现有 `cmd` CLI 并存。
- `web/admin`：Vue 3 + TypeScript + Vite 管理界面。
- 管理元数据库：保存数据源注册、租户元信息、任务、用户/角色和审计；与被管理的目标数据库严格区分。

## 关键边界

1. HTTP Handler 不直接拼接 SQL 或操作目标数据库；通过 service/use-case 调用核心包。
2. 目标数据库连接必须由受控的连接注册表管理，连接测试、迁移和分片操作均设置超时。
3. DSN 凭据不得出现在 API 响应、日志、错误详情或前端状态中；持久化前应使用由环境变量提供密钥的加密方案。
4. 迁移、回滚、删除表、批量租户迁移等操作必须进行授权、二次确认、任务互斥和审计。
5. 默认只监听 loopback 地址。管理 API 已启用登录会话、CSRF/Origin 校验和后端 RBAC；任何非本机网络部署仍必须配置 TLS、可信反向代理和额外网络访问控制。
6. 异步任务需记录状态、开始/结束时间、耗时、错误摘要和操作者；重试不得造成重复执行。
7. 核心库继续由 CLI 和 Go API 共同使用，不复制迁移、租户或分片业务逻辑。

## 阶段安排

### 阶段 1：工程基础
- 建立独立开发分支。
- 增加 Go 管理 API 服务入口、健康检查和 API 基础结构。
- 建立 Vue 3 + TypeScript + Vite 前端工程和统一后台布局。
- 明确 API 契约、配置项、目录结构和安全边界。
- 增加 Go API 单元测试与 CI 前端检查。

### 阶段 2：数据源管理
- [x] 独立 SQLite 管理元数据库与初始 schema 版本记录。
- [x] 数据源新增、列表、详情、更新、删除 API。
- [x] DSN 使用 AES-256-GCM 加密存储，API 不回显凭据。
- [x] MySQL、PostgreSQL、SQLite、ClickHouse 连接测试（5 秒超时）。
- [x] 数据源管理前端页面与 Dashboard 实际数据源数量。
- [ ] 管理元数据库 schema 升级机制完善、备份/恢复说明。
- [x] 数据源连接池注册表、连接池生命周期管理和并发访问控制。
- [x] 对各数据库驱动补充集成测试（SQLite 临时文件 + CI MySQL/PostgreSQL/ClickHouse 服务容器）。

#### 数据源连接池约定

- 管理 API 按数据源 ID 缓存 `database/sql` 连接池；同一数据源的并发请求复用同一个池。
- 池配置变化时先更新元数据，再使旧池失效；删除数据源时同步移除池。
- 连接测试通过缓存池执行 `PingContext`，单次请求超时为 5 秒；Ping 失败只淘汰本次使用的池实例，避免误关闭并发请求新建的替代池。
- 连接池限制：最多 10 个打开连接、最多 5 个空闲连接，连接最长生命周期 5 分钟。
- HTTP 服务完成优雅关闭后统一关闭目标数据库连接池。管理 API 已启用会话认证、CSRF/Origin 校验和后端 RBAC；服务仍必须保持 loopback 监听。


#### 数据库驱动集成测试

- 集成测试位于 `pkg/driver/integration`，使用 `integration` build tag，与快速单元测试分开执行。
- SQLite 使用测试临时目录中的独立数据库文件；MySQL、PostgreSQL、ClickHouse 使用独立测试服务。
- 本地可按需设置 `METEOR_TEST_MYSQL_DSN`、`METEOR_TEST_POSTGRES_DSN`、`METEOR_TEST_CLICKHOUSE_DSN` 后运行：
  `go test -tags=integration ./pkg/driver/integration -count=1 -v`。
- CI 为三个服务启动临时容器，自动运行四种驱动的 Ping、迁移记录表创建、Up、Checksum 校验、Down 和记录清理验证。
- 集成测试使用独立前缀和唯一表名；测试结束会清理迁移目标表及历史表。不要将生产 DSN 用于该测试。

### 阶段 3：Migration 管理
- [x] 按数据源读取迁移版本状态 API。
- [x] 迁移 Up Dry Run 预览 API（校验已应用脚本 Checksum，不执行用户迁移 SQL）。
- [x] Web 迁移管理页面：数据源选择、版本状态、待执行计划和可回滚标识。
- [x] 后台任务执行 Up/Down/ToVersion，支持持久化任务状态、超时、取消、操作者记录和审计事件。
- [x] Down/DownSteps/ToVersion API 显式确认；前端二次确认；按数据源串行调度并复用 Driver 数据库锁。
- [x] 数据源存在 queued/running 迁移任务时禁止更新/删除；通过 SQLite 触发器原子保护，避免任务执行期间 DSN/驱动配置变化。
- [x] 操作审计（操作者、请求来源、任务动作/状态变化）与管理员登录、会话管理、后端 RBAC。
- 迁移目录/脚本管理、版本状态、Dry Run、后台 Up、Down、DownSteps、ToVersion 任务。
- 通过 SQLite 持久化后台任务执行耗时操作，支持状态查询、取消、超时和重启后中断恢复；当前不自动重放未完成任务。
- 为危险操作加入确认、互斥和审计。

### 阶段 4：多租户管理
- [x] 管理元数据库租户注册表：名称、唯一 code、active/suspended 状态和数据源映射。
- [x] 租户 CRUD API、连接测试 API 和 Web 管理页面。
- [x] 租户引用的数据源禁止修改连接配置或删除；迁移任务执行期间禁止创建/修改/删除相关租户。
- [x] 将 pkg/tenant HeaderResolver 与管理端租户注册表、数据源连接池打通；从可信 Context 解析 active tenant，并按 data_source_id 复用连接池。
- [ ] 将租户 Manager 纳入统一连接池生命周期（当前由数据源 PoolManager 按 data_source_id 管理，避免同一数据源重复建池）。
- [ ] 动态租户发现与租户连接状态巡检。
- [x] 单租户迁移任务提交：仅允许 active 租户，持久化 tenant-task 关联，并在事务内复核租户状态与数据源映射；任务执行仍按数据源串行。
- [x] 批量租户 Up 迁移：1–100 个 active 租户预校验、独立持久化子任务、同数据源串行、跨数据源最多 4 个 Worker、单任务失败隔离和批次状态汇总。
- [x] 失败任务不自动重试；需检查目标库状态后由操作者重新提交，避免不确定 SQL 部分执行导致重复变更。
- [ ] 批次级操作审计与提交阶段失败持久化。
- [ ] 租户级审计、停用租户访问阻断和权限控制。

### 阶段 5：分库分表管理
- [x] 分片规则持久化 CRUD：按数据源和逻辑基础表唯一登记，校验表名与策略参数。
- [x] 路由预览：复用 `pkg/sharding.ShardRule`，支持时间/ID 单点和范围计算，并限制预览规模。
- [x] 物理表列表与存在性检查：按 MySQL、PostgreSQL、SQLite、ClickHouse 元数据查询适配，按策略校验表名后缀，5 秒超时，只读不执行 DDL。
- [x] 分片表结构克隆：DDL 预览、源/目标存在性预检、显式 confirm、异步任务、超时/取消、重启后 interrupted 恢复和事务审计；同一数据源与迁移任务互斥；MySQL/PostgreSQL/ClickHouse 复制结构不复制数据，SQLite CTAS 仅复制列定义。
- [x] 分片 DDL 任务列表、详情和审计查询 API/Web 交互；当前只开放结构克隆，不开放数据复制、覆盖已有表或自动 DROP。

### 阶段 6：任务、审计与权限
- [x] 统一任务中心：聚合迁移任务与分片 DDL 任务，支持类型/状态筛选、活动任务自动刷新、详情、执行进度、取消和审计时间线。
- [x] 独立操作日志检索与跨任务审计筛选（类型、状态、任务、数据源、租户、分片规则和时间范围）。
- [x] 用户与角色管理：viewer、migration_operator、shard_operator、administrator；前端操作按后端权限矩阵显示。
- [x] 取消、超时、并发互斥、重启恢复和安全失败诊断。

### 阶段 7：交付
- 单元/集成测试、前端构建、CI、部署与升级文档。
- Docker/二进制部署方案、配置样例和安全加固指南。

## 首期验收标准

- `go test ./...`、`go vet ./...`、`go build ./...` 通过。
- 管理服务默认仅监听本机，提供健康检查 API。
- 前端可独立启动并通过 Vite 代理访问 API。
- 页面具备 Dashboard、数据源、迁移、租户、分片、任务/审计导航；Dashboard 使用真实 API 统计，不显示模拟数据。
- API 契约、配置说明和本地开发文档齐全。
- 未接入真实数据源前，界面明确标识尚未配置，不展示伪造的业务统计。
