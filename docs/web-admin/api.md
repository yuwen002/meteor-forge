# 管理 API：数据源

Base URL：`/api/v1`

当前接口为本地开发预览接口。管理服务默认绑定 `127.0.0.1:8081`，尚未提供用户认证和角色授权，不得暴露到公网。

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

`status` 可省略，默认为 `active`。成功返回 HTTP 201。租户必须引用已登记的数据源；重复 code 返回 HTTP 409。

### GET /tenants/{id}

读取租户详情，不包含 DSN。

### PUT /tenants/{id}

请求字段与创建接口一致，可修改租户名称、code、数据源映射和状态。更新成功返回 HTTP 200。

### DELETE /tenants/{id}

删除管理端租户登记，返回 HTTP 204。不会删除目标数据库、目标库中的数据或被引用的数据源。

### POST /tenants/{id}/test

使用租户关联数据源的连接池执行 Ping，超时 5 秒。失败响应只返回通用提示，不暴露驱动错误或 DSN。

### 租户与数据源的生命周期保护

- 被任一租户引用的数据源不能更新连接配置或删除，返回 HTTP 409（`data_source_has_tenants`）。请先调整或删除租户映射。
- 当租户当前或目标数据源存在 queued/running 迁移任务时，禁止创建、更新或删除该租户，返回 HTTP 409（`tenant_has_active_migration`）。
- 多个租户可以引用同一数据源；此时它们共享该数据源的连接池。需要数据库级隔离时，应为每个租户登记独立数据源。
- `Server.ResolveTenantDB(ctx)` 已将 `pkg/tenant.HeaderResolver`、租户注册表和数据源连接池打通：从可信上下文读取 tenant ID，校验租户存在且状态为 `active`，再返回该租户映射数据源的共享 `*sql.DB` 连接池。同一数据源下的多个租户复用同一连接池。
- `ResolveTenantDB` 是供后续业务 Handler/服务集成的内部入口，不是 HTTP API。调用方必须由已认证、已授权的上游中间件将 tenant ID 写入 Context；禁止直接信任未经验证的 Header、Query 或 Body。缺少上下文、租户不存在或已停用时拒绝解析。
- 当前尚未接入真实认证/授权中间件或业务数据访问路由；管理 API 仍未实现用户认证/授权，不得暴露公网。

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

**重要：** 当前迁移驱动的历史表初始化通过 `EnsureTable` 完成，因此首次读取状态或生成预览时，可能在目标数据库创建 `schema_migrations` 历史表；这两个接口不会执行用户迁移脚本中的 DDL/DML。执行 Up/Down/ToVersion 的异步任务 API 尚未开放。


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

Down、DownSteps 和 ToVersion 必须显式提交 `confirm: true`。前端还会显示二次确认对话框。提交前请备份目标数据库并检查迁移脚本。任务取消不等于数据库回滚：取消时已经完成的迁移步骤不会自动撤销。

### GET /migration-tasks

返回最近 50 条任务记录，按创建时间倒序。

### POST /tenants/{id}/migrations/tasks

为指定的 active 租户提交迁移任务，请求体与 `POST /datasources/{id}/migrations/tasks` 相同，返回 HTTP 202。任务响应额外包含 `tenant_id`，并在管理元数据库中持久化租户与任务的关联；全局任务列表和详情也会返回该字段。已停用租户返回 HTTP 409，不创建任务。提交时会在同一事务内再次校验租户仍为 active 且仍映射到该数据源，避免校验与入队之间的状态竞态。

**迁移作用范围：** 租户 ID 用于校验、任务归属和审计追踪；当前迁移脚本仍由 `pkg/migration` 在租户映射的数据源上执行，属于数据库级迁移，不会自动按租户过滤 SQL。多个租户若映射到同一个数据源，会共享同一套 `schema_migrations` 版本记录，并按数据源串行执行；因此同一物理数据库上的迁移只会应用一次。需要独立迁移历史时，应为租户配置独立数据源/数据库。

### GET /migration-tasks/{id}

读取单个任务状态。

任务状态：`queued`、`running`、`succeeded`、`failed`、`canceled`、`interrupted`。任务详情和列表同时返回进度字段：`current_version`（当前迁移版本）、`completed_steps`、`total_steps`、`current_action`（`up` / `down`）和 `progress_updated_at`。进度在每条迁移开始及成功完成时写入管理元数据库；没有待执行迁移时这些计数保持为 0。开始时间与结束时间可用于计算任务耗时。进度只反映已成功完成的迁移步骤，不代表单条 SQL 内部的执行百分比；单条 SQL 执行期间当前版本会保持不变。状态转换由管理元数据库原子约束：`queued` 只能进入 `running`、`failed`、`canceled` 或 `interrupted`；`running` 只能进入 `succeeded`、`failed`、`canceled` 或 `interrupted`；终态不可重新打开或改写。无效转换会被拒绝。任务响应只包含安全的状态摘要，不包含 DSN、SQL 内容或数据库驱动原始错误。

### GET /migration-tasks/{id}/audit

按时间顺序返回任务审计事件，包含数据源 ID、动作、状态变更前后值、操作者标识、请求来源、事件摘要和时间戳。当前尚未实现用户认证，操作者固定标记为 `local-api`，不能视为真实登录用户；请求来源取管理 API 连接的远端地址。事件与任务创建/状态变更在同一 SQLite 事务内写入，避免任务状态更新成功但审计事件丢失。 服务启动时将遗留的 queued/running 任务标记为 interrupted 时，也会写入对应的恢复审计事件。

### POST /migration-tasks/{id}/cancel

请求取消当前服务实例中正在排队或执行的任务，返回 HTTP 202。任务执行上下文会被取消；具体数据库驱动和 SQL 执行是否能立即响应取消取决于驱动实现。排队任务会在获得数据源执行锁后检查取消状态，不会开始执行迁移 SQL。若任务已完成或不在当前实例活动列表中，返回 HTTP 409。

服务关闭时，任务管理器会取消所有排队和执行中的任务，并等待 Worker 释放数据源锁，默认最多等待 10 秒。若底层驱动忽略 context 取消，关闭等待会返回超时；此时服务不会提前关闭仍可能被 Worker 使用的目标数据库连接池。进程退出后遗留任务将在下次启动时标记为 `interrupted`，并写入审计事件。取消和关闭都不会自动回滚已经成功完成的迁移步骤。

### 数据源变更与迁移任务互斥

当某个数据源存在 `queued` 或 `running` 状态的迁移任务时，更新或删除该数据源会返回 HTTP 409：

```json
{"code":"data_source_has_active_tasks","message":"the data source cannot be changed or deleted while migration tasks are queued or running"}
```

任务进入 `succeeded`、`failed`、`canceled` 或 `interrupted` 等终态后，数据源才可更新或删除。该约束由管理元数据库触发器在写入时原子检查，不依赖 HTTP Handler 的先查后改，因此并发请求也不能绕过保护。删除数据源仍只删除管理端登记，不会删除目标数据库。
