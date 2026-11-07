# Web 管理后台 API 参考

Base URL：`/api/v1`

管理 API 默认绑定 `127.0.0.1:8081`，需要先完成管理员初始化并通过 HttpOnly 会话 Cookie 登录。后端强制执行 RBAC 和同源校验；服务当前只接受 loopback 监听地址，不得暴露到公网。

## 数据模型

数据源响应仅包含：

| 字段 | 类型 | 说明 |
|---|---|---|
| id | string | 管理端生成的唯一 ID |
| name | string | 数据源显示名称 |
| driver | string | mysql / postgres / sqlite / clickhouse |
| created_at | RFC3339 | 创建时间 |
| updated_at | RFC3339 | 更新时间 |

DSN 仅在创建和更新请求中提交。服务端使用 AES-256-GCM 加密后存入管理元数据库；任何读取接口都不返回 DSN。

## 接口

### GET /datasources

返回 `{"items": DataSource[]}`。

### POST /datasources

请求：

```json
{
  "name": "local mysql",
  "driver": "mysql",
  "dsn": "user:password@tcp(127.0.0.1:3306)/app"
}
```

成功返回 HTTP 201 和脱敏的数据源元信息。重复名称返回 409。

### GET /datasources/{id}

返回数据源元信息，不包含 DSN。

### PUT /datasources/{id}

请求字段与创建接口一致。更新时必须提交新的完整 DSN；旧 DSN 不会回显。

### DELETE /datasources/{id}

删除管理端登记，不会删除目标数据库或其中的数据。成功返回 HTTP 204。

### POST /datasources/{id}/test

测试已保存的连接，超时 5 秒。返回：

```json
{"success": true, "message": "connection successful"}
```

失败时只返回通用错误信息，不回传驱动原始错误，以免泄漏连接串或服务端信息。

## 多租户管理

租户记录保存在管理元数据库中。每个租户通过 `data_source_id` 引用一个已登记的数据源，不重复保存 DSN；租户 API 只返回数据源名称和驱动类型，不返回连接凭据。租户标识 `code` 会归一化为小写，并且全局唯一；允许字符为字母、数字、短横线和下划线。状态为 `active` 或 `suspended`，创建时默认 `active`。

### GET /tenants

返回租户列表：`{"items": Tenant[]}`。

### POST /tenants

请求：

```json
{
  "name": "Acme Workspace",
  "code": "acme-prod",
  "data_source_id": "registered-data-source-id",
  "status": "active"
}
```

`status` 可省略，默认为 `active`。成功返回 HTTP 201。租户必须引用已登记的数据源；重复 code 返回 HTTP 409。创建动作与租户审计事件在同一事务中提交。

### GET /tenants/{id}

读取租户详情，不包含 DSN。

### PUT /tenants/{id}

请求字段与创建接口一致，可修改租户名称、code、数据源映射和状态。更新成功返回 HTTP 200。更新前后非敏感租户元数据与操作者审计事件在同一事务中提交。

### DELETE /tenants/{id}

删除管理端租户登记，返回 HTTP 204。不会删除目标数据库、目标库中的数据或被引用的数据源。删除前快照与审计事件在同一事务中提交，租户删除后仍可通过审计接口查询历史。

### GET /tenants/{id}/audit

返回该租户的生命周期审计记录：`{"items": TenantAuditEvent[], "total": number, "limit": number, "offset": number}`。支持 `limit`（1–200，默认 50）和 `offset`（默认 0）。

每条事件包含动作（created/updated/deleted）、操作者、请求来源、发生时间，以及变更前/后的非敏感快照（名称、code、数据源 ID、状态）。快照不包含 DSN 或连接凭据；即使租户已删除，历史记录仍保留。审计写入失败会回滚对应租户变更。

### POST /tenants/{id}/test

使用租户关联数据源的连接池执行 Ping，超时 5 秒。失败响应只返回通用提示，不暴露驱动错误或 DSN。

### 租户与数据源的生命周期保护

- 被任一租户引用的数据源不能更新连接配置或删除，返回 HTTP 409（`data_source_has_tenants`）。请先调整或删除租户映射。
- 当租户当前或目标数据源存在 queued/running 迁移任务时，禁止创建、更新或删除该租户，返回 HTTP 409（`tenant_has_active_migration`）。
- 多个租户可以引用同一数据源；此时它们共享该数据源的连接池。需要数据库级隔离时，应为每个租户登记独立数据源。
- `Server.ResolveTenantDB(ctx)` 已将 `pkg/tenant.HeaderResolver`、租户注册表和数据源连接池打通：从可信上下文读取 tenant ID，校验租户存在且状态为 `active`，再返回该租户映射数据源的共享 `*sql.DB` 连接池。同一数据源下的多个租户复用同一连接池。
- `ResolveTenantDB` 是供后续业务 Handler/服务集成的内部入口，不是 HTTP API。调用方必须由已认证、已授权的上游中间件将 tenant ID 写入 Context；禁止直接信任未经验证的 Header、Query 或 Body。缺少上下文、租户不存在或已停用时拒绝解析。
- 租户解析器本身不替代 API 认证/授权。管理 API 已接入会话认证和后端 RBAC；业务数据访问路由仍需由调用方在可信上下文中传入租户 ID。

## 管理元数据库配置

- `METEOR_ADMIN_DB`：SQLite 文件路径；默认位于操作系统用户配置目录的 `meteor-forge/admin.db`。
- `METEOR_ADMIN_ENCRYPTION_KEY`：必填，64 位十六进制字符串（32 字节）。
- 可用 `openssl rand -hex 32` 生成密钥。必须备份密钥；丢失后无法解密已存储 DSN。
- SQLite 文件权限设置为仅当前用户可读写（0600，受操作系统权限模型约束）。
- 管理元数据库与被管理的目标数据库分离。

## 错误响应

统一结构：

```json
{"code": "not_found", "message": "data source not found"}
```

错误响应不会包含 DSN、密码或数据库驱动的原始错误文本。

## Migration 状态与预览

迁移脚本目录由服务端配置项 `METEOR_MIGRATIONS_DIR` 指定，默认是当前工作目录下的 `migrations`。脚本命名规则：

- `{version}_{description}.up.sql`
- `{version}_{description}.down.sql`

### GET /datasources/{id}/migrations/status

读取指定数据源的本地迁移脚本状态，返回：

```json
{
  "data_source_id": "datasource-id",
  "items": [
    {"version":"000001","description":"create_users","is_applied":false}
  ],
  "applied_count": 0,
  "pending_count": 1
}
```

### POST /datasources/{id}/migrations/preview

生成待执行 Up 计划，不会执行迁移脚本 SQL。响应包含版本、说明、动作、是否存在 Down 脚本及脚本 SHA-256 校验和。若已应用脚本与本地脚本校验和不一致，返回 HTTP 409 并阻止生成计划。

**重要：** 当前迁移驱动的历史表初始化通过 `EnsureTable` 完成，因此首次读取状态或生成预览时，可能在目标数据库创建 `schema_migrations` 历史表；这两个接口不会执行用户迁移脚本中的 DDL/DML。异步任务接口需要登录；Up 任务仅允许 migration_operator 或 administrator，Down/DownSteps/ToVersion 仅允许 administrator。


## Migration 后台执行任务

后台执行任务记录保存在管理元数据库中。服务重启后，仍处于 queued/running 的任务会标记为 interrupted；不会自动重放，避免不确定的 SQL 部分执行状态被重复应用。任务执行超时为 30 分钟，同一数据源的任务在当前服务实例内串行执行，核心 Migration Driver 仍会获取数据库级迁移锁。

### POST /datasources/{id}/migrations/tasks

提交后台迁移任务，返回 HTTP 202 和任务记录。

Up 请求：

```json
{"action":"up"}
```

回滚最近一次：

```json
{"action":"down","confirm":true}
```

连续回滚指定步数（1–100）：

```json
{"action":"down_steps","steps":2,"confirm":true}
```

跳转到目标版本；`0` 表示回滚全部已应用迁移：

```json
{"action":"to_version","target_version":"000010","confirm":true}
```

Down、DownSteps 和 ToVersion 必须显式提交 `confirm: true`，并且仅 administrator 角色可提交。前端还会显示二次确认对话框。提交前请备份目标数据库并检查迁移脚本。任务取消不等于数据库回滚：取消时已经完成的迁移步骤不会自动撤销。

### GET /migration-tasks

返回最近 50 条任务记录，按创建时间倒序。

### POST /tenants/{id}/migrations/tasks

为指定的 active 租户提交迁移任务，请求体与 `POST /datasources/{id}/migrations/tasks` 相同，返回 HTTP 202。任务响应额外包含 `tenant_id`，并在管理元数据库中持久化租户与任务的关联；全局任务列表和详情也会返回该字段。已停用租户返回 HTTP 409，不创建任务。提交时会在同一事务内再次校验租户仍为 active 且仍映射到该数据源，避免校验与入队之间的状态竞态。

**迁移作用范围：** 租户 ID 用于校验、任务归属和审计追踪；当前迁移脚本仍由 `pkg/migration` 在租户映射的数据源上执行，属于数据库级迁移，不会自动按租户过滤 SQL。多个租户若映射到同一个数据源，会共享同一套 `schema_migrations` 版本记录，并按数据源串行执行；因此同一物理数据库上的迁移只会应用一次。需要独立迁移历史时，应为租户配置独立数据源/数据库。

### POST /tenant-migration-batches

批量为多个租户提交 Up 迁移任务。请求：

```json
{"tenant_ids":["tenant-id-1","tenant-id-2"]}
```

- 每批 1–100 个租户，ID 不允许重复。
- 请求先整体校验租户存在且均为 active；校验不通过时不会创建任何子任务。
- 任务提交阶段采用失败隔离：单个租户提交失败会记录在响应的 `failures`，继续处理后续租户；批次记录在创建首个子任务前持久化，因此即使所有子任务提交失败，批次仍可查询。
- 每个子任务单独持久化、单独审计，包含共同的 `batch_id`。同一数据源的子任务仍串行；不同数据源最多同时执行 4 个迁移 Worker。
- 批量接口只开放 Up，不提供批量回滚，避免一次误操作影响多个租户。
- 失败的迁移不会自动重试。迁移可能已经执行部分步骤，必须先检查目标库的迁移历史和实际结构，再由操作者决定是否重新提交。

成功返回 HTTP 202，包含批次 ID、提交数量、提交失败数量、子任务列表、发起人/请求来源和批次审计事件。`audit_persisted` 表示提交结果及批次审计是否已原子持久化；若为 false，响应会带安全提示，批次保留在 `submitting` 状态，应先检查子任务再决定是否重试。

### GET /tenant-migration-batches/{id}

根据持久化的 `batch_id` 返回批次元数据、子任务和汇总状态：queued、running、succeeded、failed、canceled、interrupted。失败、取消或重启中断的租户，以及在创建子任务阶段失败的租户，都会列在 `failures` 中。响应还包含发起人、请求来源、提交失败数量和批次审计事件（`submission_started`、`submission_completed`）。即使没有任何子任务成功创建，也能读取批次和提交失败详情。

### GET /migration-tasks/{id}

读取单个任务状态。

任务状态：`queued`、`running`、`succeeded`、`failed`、`canceled`、`interrupted`。任务详情和列表同时返回进度字段：`current_version`（当前迁移版本）、`completed_steps`、`total_steps`、`current_action`（`up` / `down`）和 `progress_updated_at`。进度在每条迁移开始及成功完成时写入管理元数据库；没有待执行迁移时这些计数保持为 0。开始时间与结束时间可用于计算任务耗时。进度只反映已成功完成的迁移步骤，不代表单条 SQL 内部的执行百分比；单条 SQL 执行期间当前版本会保持不变。状态转换由管理元数据库原子约束：`queued` 只能进入 `running`、`failed`、`canceled` 或 `interrupted`；`running` 只能进入 `succeeded`、`failed`、`canceled` 或 `interrupted`；终态不可重新打开或改写。无效转换会被拒绝。任务响应只包含安全的状态摘要，不包含 DSN、SQL 内容或数据库驱动原始错误。

### GET /migration-tasks/{id}/audit

按时间顺序返回任务审计事件，包含数据源 ID、租户 ID（租户级任务）、动作、状态变更前后值、操作者标识、请求来源、事件摘要和时间戳。当前任务审计事件记录已认证的管理员用户名；请求来源取管理 API 连接的远端地址，不信任转发头。认证登录、登出、密码变更和管理员账号生命周期另记入管理员专属安全审计流。事件与任务创建/状态变更在同一 SQLite 事务内写入，避免任务状态更新成功但审计事件丢失。 服务启动时将遗留的 queued/running 任务标记为 interrupted 时，也会写入对应的恢复审计事件。

### POST /migration-tasks/{id}/cancel

请求取消当前服务实例中正在排队或执行的任务，返回 HTTP 202。任务执行上下文会被取消；具体数据库驱动和 SQL 执行是否能立即响应取消取决于驱动实现。排队任务会在获得数据源执行锁后检查取消状态，不会开始执行迁移 SQL。若任务已完成或不在当前实例活动列表中，返回 HTTP 409。

服务关闭时，任务管理器会取消所有排队和执行中的任务，并等待 Worker 释放数据源锁，默认最多等待 10 秒。若底层驱动忽略 context 取消，关闭等待会返回超时；此时服务不会提前关闭仍可能被 Worker 使用的目标数据库连接池。进程退出后遗留任务将在下次启动时标记为 `interrupted`，并写入审计事件。取消和关闭都不会自动回滚已经成功完成的迁移步骤。

### 数据源变更与迁移任务互斥

当某个数据源存在 `queued` 或 `running` 状态的迁移任务时，更新或删除该数据源会返回 HTTP 409：

```json
{"code":"data_source_has_active_tasks","message":"the data source cannot be changed or deleted while migration tasks are queued or running"}
```

任务进入 `succeeded`、`failed`、`canceled` 或 `interrupted` 等终态后，数据源才可更新或删除。该约束由管理元数据库触发器在写入时原子检查，不依赖 HTTP Handler 的先查后改，因此并发请求也不能绕过保护。删除数据源仍只删除管理端登记，不会删除目标数据库。


## 分库分表规则与路由预览

分片规则保存在独立管理元数据库中，只保存逻辑表、目标数据源和分片算法参数，不保存 DSN。规则 CRUD 不会自动创建、修改或删除目标数据库中的物理表。

支持的策略：

| shard_type | 说明 | 参数 |
|---|---|---|
| year | 按年 | 无 |
| quarter | 按季度 | 无 |
| month | 按月 | 无 |
| week | 按 ISO 周 | 无 |
| ten_day | 按旬（1–10、11–20、21–月底） | 无 |
| id_hash | CRC32 哈希取模 | hash_shards，默认 16，范围 1–4096 |
| id_range | ID 整数范围分片 | range_size，默认 1,000,000，必须为正数 |

逻辑表名只允许字母、数字和下划线，必须以字母或下划线开头，长度不超过 40 个字符，以便为生成的分片后缀预留空间。相同数据源下的基础表名唯一。 已被分片规则引用的数据源不能修改连接配置（驱动或 DSN）或删除，返回 HTTP 409（`data_source_has_shard_rules`）；请先删除对应规则配置。该保护只限制管理端配置变更，不影响目标数据库中的现有物理表。

### GET /shard-rules

返回 `{"items": ShardRule[]}`。每条规则包含 ID、数据源安全元信息、基础表名、策略类型和参数，不包含 DSN。

### POST /shard-rules

创建分片规则，成功返回 HTTP 201。请求示例：

```json
{
  "data_source_id": "registered-data-source-id",
  "base_table": "orders",
  "shard_type": "month"
}
```

哈希分片示例：

```json
{
  "data_source_id": "registered-data-source-id",
  "base_table": "users",
  "shard_type": "id_hash",
  "hash_shards": 32
}
```

ID 范围分片示例：

```json
{
  "data_source_id": "registered-data-source-id",
  "base_table": "events",
  "shard_type": "id_range",
  "range_size": 500000
}
```

### GET /shard-rules/{id}

读取规则详情。

### PUT /shard-rules/{id}

使用与创建接口相同的请求结构替换规则配置。目标数据源必须已登记。

### DELETE /shard-rules/{id}

只删除管理元数据库中的规则配置，返回 HTTP 204。不会删除任何目标物理表或数据。存在 queued/running DDL 任务时返回 HTTP 409。终态任务及审计记录独立保留，删除规则不会删除历史任务或审计记录。

### POST /shard-rules/{id}/preview

调用 `pkg/sharding.ShardRule` 计算目标表名，不连接目标数据库、不检查物理表是否存在，也不执行 SQL。

时间策略支持单点或闭区间预览：

```json
{"timestamp":"2026-11-05T09:00:00+08:00"}
```

```json
{"start_time":"2026-10-01T00:00:00Z","end_time":"2026-11-30T23:59:59Z"}
```

ID 策略支持单点或闭区间预览：

```json
{"id":123456}
```

```json
{"start_id":100000,"end_id":299999}
```

响应包含 `shard_rule_id`、`data_source_id`、`base_table`、`shard_type`、`tables` 和 `table_count`。时间范围最多 10 年，ID 范围策略最多预览 1000 张表，所有预览最多返回 4096 张表；超限返回 HTTP 400。ID 必须为非负整数，时间必须为 RFC3339。

### GET /shard-rules/{id}/tables

通过已登记数据源的连接池读取当前数据库元数据，并按分片策略校验表名后缀，返回符合该规则命名约定的物理表名列表和数量。支持 MySQL、PostgreSQL、SQLite、ClickHouse；单次元数据读取超时为 5 秒。该接口不执行 DDL。

### POST /shard-rules/{id}/tables/exists

检查指定物理表是否存在。请求：

```json
{"table_name":"orders_202611"}
```

表名必须符合该规则对应的后缀格式（例如 month 策略为 `{base}_{YYYY}{MM}`，id_hash 策略的序号必须位于配置范围内）。无效表名返回 HTTP 400；有效但不存在的表返回 HTTP 200 和 `{"exists":false}`。

### POST /shard-rules/{id}/clone-preview

检查克隆参数、源表是否存在、目标表是否已存在，并返回按数据库驱动生成的 DDL。请求：

```json
{"source_table":"orders","target_table":"orders_202611"}
```

源表可以是逻辑基础表或符合当前规则命名约定的物理分片表；目标表必须是符合该策略的物理分片表，且不能与源表相同。源表不存在或目标表已存在返回 HTTP 409。预览只读，不执行 DDL。

### POST /shard-rules/{id}/clone-tasks

提交异步结构克隆任务，必须显式提供 `confirm: true`：

```json
{"source_table":"orders","target_table":"orders_202611","confirm":true}
```

成功返回 HTTP 202 和任务记录。执行前会再次检查源表存在、目标表不存在，避免预览与执行之间的状态变化导致覆盖。任务超时 10 分钟，跨数据源最多同时执行 2 个 DDL Worker；同一数据源的克隆任务串行执行。同一数据源的迁移任务与分片 DDL 任务互斥，管理元数据库触发器在入队时原子检查，冲突返回 HTTP 409。服务重启后遗留的 queued/running 任务标记为 interrupted，不会自动重放。失败、取消或中断后必须检查目标库实际结构再决定是否重新提交。

### GET /shard-ddl-tasks

返回最近 50 条分片 DDL 任务。

### GET /shard-ddl-tasks/{id}

读取单条分片 DDL 任务状态。状态包括 queued、running、succeeded、failed、canceled、interrupted。

### GET /shard-ddl-tasks/{id}/audit

按时间顺序读取该任务的状态审计事件。任务入队、执行状态变更和重启恢复会在同一管理元数据库事务中记录。操作者字段为提交任务时已认证的管理员用户名；请求来源取管理 API 连接的直接远端地址，不信任转发头。

### POST /shard-ddl-tasks/{id}/cancel

请求取消当前服务实例中的活动任务，返回 HTTP 202。取消不保证底层数据库立即中断 DDL；必须重新盘点目标表确认最终状态。

**克隆语义与限制：**
- MySQL 使用 `CREATE TABLE ... LIKE ...`，复制列定义、索引和生成列，不复制数据、外键定义或触发器。
- PostgreSQL 使用 `LIKE ... INCLUDING ALL`，复制列、默认值、检查/非空约束、索引和存储属性，不复制数据、外键约束或触发器。
- ClickHouse 使用 `CREATE TABLE ... AS ...`，复制表结构/引擎定义，不复制数据。
- SQLite 使用 CTAS（`CREATE TABLE ... AS SELECT ... WHERE 0`），仅复制列结构，不复制主键、约束、索引和触发器。生产环境需要这些对象时，应在克隆后通过独立、受审计的迁移补齐。
- 当前只开放结构克隆，不提供数据复制、覆盖已有表、DROP 或自动清理。

除公开健康检查与登录接口外，所有管理 API 均要求有效会话，并由后端 RBAC 强制授权。完整角色矩阵见 [角色权限矩阵](../web-admin/permissions.md)，认证细节见 [认证与安全说明](../web-admin/authentication.md)。管理 API 当前强制 loopback 监听，不得暴露公网。


## 认证、健康检查与操作审计接口索引

### GET /health

公开的最小存活接口，返回服务状态、API 版本和 UTC 时间戳。不检查目标数据源连通性，也不暴露配置。

### 认证与管理员接口

| 方法 | 路径 | 权限 |
|---|---|---|
| POST | `/auth/login` | 公开、登录限流 |
| GET | `/auth/session` | 已登录 |
| POST | `/auth/logout` | 已登录、Origin/CSRF 校验 |
| POST | `/auth/password` | 已登录、当前密码校验、Origin/CSRF 校验 |
| GET | `/auth/users` | administrator |
| POST | `/auth/users` | administrator |
| PUT | `/auth/users/{id}/role` | administrator |
| DELETE | `/auth/users/{id}` | administrator |
| GET | `/auth/security-events` | administrator |

登录请求为 JSON：`{"username":"admin","password":"<password>"}`。成功后返回 HttpOnly 会话 Cookie；响应体只返回用户名、角色和过期时间。退出和修改密码会执行 Origin/CSRF 校验。

### GET /audit-events

读取迁移与分片 DDL 的操作审计事件。支持筛选参数：

- `kind`：`migration` 或 `shard_ddl`。
- `status`：queued、running、succeeded、failed、canceled、interrupted。
- `task_id`、`data_source_id`、`tenant_id`、`shard_rule_id`。
- `from`、`to`：RFC3339 时间戳，from 不得晚于 to。
- `limit`：1–200，默认 50；`offset`：0–10,000,000，默认 0。

响应包含 `items`、`total`、`limit`、`offset`。所有已认证角色可读取操作审计；安全审计事件与此接口分离，仅管理员可读取。

## 租户连接健康巡检 API

### 租户连接健康巡检

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | `/api/v1/tenant-health` | 所有已认证角色 | 查询每个租户最近一次巡检结果 |
| POST | `/api/v1/tenants/{id}/health-check` | administrator | 巡检单个租户连接 |
| POST | `/api/v1/tenant-health-checks` | administrator | 批量巡检，`tenant_ids` 必须为 1–100 个唯一租户 ID |

批量请求示例：

```json
{"tenant_ids":["tenant-id-1","tenant-id-2"]}
```

巡检最多 4 个并发，每个连接检查最多 5 秒。结果包含 `tenant_id`、`status`（healthy/unhealthy）、`checked_at`、`duration_ms` 和脱敏后的 `message`。管理元数据库只保留每个租户最近一次结果；暂不提供历史趋势和定时调度。

原有 `POST /api/v1/tenants/{id}/test` 保持兼容，现同样会记录最近一次巡检结果。巡检失败不会自动停用租户、修改租户配置或触发修复。原始驱动错误、DSN 和密码不会写入响应或巡检记录。
