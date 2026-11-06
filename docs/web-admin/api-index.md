# Web 管理 API 接口目录与权限矩阵

Base URL：/api/v1

除健康检查和登录外，管理 API 需要有效管理员会话。认证使用 HttpOnly Cookie。写操作受 Origin/CSRF 校验和后端 RBAC 约束。管理 API 默认仅监听 loopback。

## 1. 角色权限矩阵

| 功能 | viewer | migration_operator | shard_operator | administrator |
|---|---:|---:|---:|---:|
| 查看数据源、租户、迁移、分片和任务 | 是 | 是 | 是 | 是 |
| 测试数据源/租户连接 | 是 | 是 | 是 | 是 |
| 提交数据库 Up 迁移 | 否 | 是 | 否 | 是 |
| 取消迁移任务 | 否 | 是 | 否 | 是 |
| Down、DownSteps、ToVersion 回滚 | 否 | 否 | 否 | 是 |
| 查看迁移、租户、操作审计 | 是 | 是 | 是 | 是 |
| 创建/修改/删除数据源 | 否 | 否 | 否 | 是 |
| 创建/修改/删除租户、租户发现导入 | 否 | 否 | 否 | 是 |
| 管理分片规则 | 否 | 否 | 否 | 是 |
| 分片路由预览、物理表盘点 | 是 | 是 | 是 | 是 |
| 分片结构克隆和取消 DDL 任务 | 否 | 否 | 是 | 是 |
| 管理员账号与安全审计 | 否 | 否 | 否 | 是 |

说明：权限由服务端中间件执行，前端按钮显隐不是安全边界。某些接口还会进行资源状态校验，例如租户必须 active、数据源不能仍被租户引用、危险操作必须显式确认。

## 2. 公共与认证接口

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | /health | 公共 | 最小健康状态，不检查目标库 |
| POST | /auth/login | 公共、限流 | 校验用户名密码并签发会话 Cookie |
| GET | /auth/session | 已登录 | 当前管理员身份和会话过期时间 |
| POST | /auth/logout | 已登录 + Origin/CSRF | 撤销当前会话 |
| POST | /auth/password | 已登录 + Origin/CSRF | 校验旧密码后修改密码并撤销会话 |

## 3. 数据源接口

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | /datasources | 已登录 | 数据源列表，不含 DSN |
| POST | /datasources | administrator | 创建数据源 |
| GET | /datasources/{id} | 已登录 | 数据源详情，不含 DSN |
| PUT | /datasources/{id} | administrator | 更新数据源，需提交完整新 DSN |
| DELETE | /datasources/{id} | administrator | 删除登记，不删除目标数据库 |
| POST | /datasources/{id}/test | 已登录 | 测试连接，5 秒超时 |
| GET | /datasources/{id}/migrations/status | 已登录 | 读取迁移版本状态 |
| POST | /datasources/{id}/migrations/preview | migration_operator/administrator | 预览待执行迁移 |
| POST | /datasources/{id}/migrations/tasks | migration_operator/administrator | 提交迁移任务 |

## 4. 租户接口

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | /tenants | 已登录 | 租户列表 |
| POST | /tenants | administrator | 创建租户 |
| GET | /tenants/{id} | 已登录 | 租户详情 |
| PUT | /tenants/{id} | administrator | 更新租户 |
| DELETE | /tenants/{id} | administrator | 删除租户登记 |
| GET | /tenants/{id}/audit | 已登录 | 租户生命周期审计 |
| POST | /tenants/{id}/test | 已登录 | 测试租户连接 |
| POST | /tenants/{id}/migrations/tasks | migration_operator/administrator | 提交租户 Up 迁移 |
| POST | /tenant-migration-batches | migration_operator/administrator | 批量提交租户 Up，1–100 条 |
| GET | /tenant-migration-batches/{id} | 已登录 | 批次详情 |
| GET | /tenant-discovery/preview | administrator | 预览控制库租户 |
| POST | /tenant-discovery/sync | administrator | 显式确认后导入，最多 500 条 |

## 5. 迁移任务接口

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | /migration-tasks | 已登录 | 最近迁移任务 |
| GET | /migration-tasks/{id} | 已登录 | 迁移任务详情 |
| POST | /migration-tasks/{id}/cancel | migration_operator/administrator | 请求取消任务 |
| GET | /migration-tasks/{id}/audit | 已登录 | 任务审计 |

创建迁移任务的 POST /datasources/{id}/migrations/tasks 请求体 action 支持 up、down、down_steps、to_version。回滚动作必须显式 confirm=true，且仅 administrator 可执行。租户迁移任务当前只提供 Up。

## 6. 分片规则与物理表接口

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | /shard-rules | 已登录 | 分片规则列表 |
| POST | /shard-rules | administrator | 创建规则 |
| GET | /shard-rules/{id} | 已登录 | 规则详情 |
| PUT | /shard-rules/{id} | administrator | 更新规则 |
| DELETE | /shard-rules/{id} | administrator | 删除规则 |
| POST | /shard-rules/{id}/preview | 已登录 | 计算逻辑路由 |
| GET | /shard-rules/{id}/tables | 已登录 | 盘点物理分片表 |
| POST | /shard-rules/{id}/tables/exists | 已登录 | 检查物理表 |
| POST | /shard-rules/{id}/clone-preview | shard_operator/administrator | 预览物理表结构克隆 |
| POST | /shard-rules/{id}/clone-tasks | shard_operator/administrator | 确认并提交结构克隆任务 |

结构克隆只复制表结构且不同驱动语义不同，不复制业务数据。请阅读 API 详细说明和分片文档，并在生产执行前核对克隆预览。

## 7. 分片 DDL 与操作审计接口

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | /shard-ddl-tasks | 已登录 | 分片 DDL 任务列表 |
| GET | /shard-ddl-tasks/{id} | 已登录 | 任务详情 |
| GET | /shard-ddl-tasks/{id}/audit | 已登录 | 任务审计 |
| POST | /shard-ddl-tasks/{id}/cancel | shard_operator/administrator | 请求取消 DDL 任务 |
| GET | /audit-events | 已登录 | 迁移和分片操作审计 |
| GET | /auth/security-events | administrator | 认证和管理员生命周期安全审计 |
| GET | /auth/users | administrator | 管理员列表 |
| POST | /auth/users | administrator + Origin/CSRF | 创建管理员 |
| PUT | /auth/users/{id}/role | administrator + Origin/CSRF | 修改角色 |
| DELETE | /auth/users/{id} | administrator + Origin/CSRF | 删除管理员 |

## 8. 通用约定

- 所有路径省略 Base URL 前缀时，实际请求应加 /api/v1。
- JSON 请求使用 application/json。
- 成功响应通常为 JSON；删除成功通常返回 204。
- 错误响应通常包含 code/message 或 error 字段，具体以各 handler 为准。
- 列表接口通常返回 items；分页审计接口还返回 total、limit、offset。
- API 不应返回 DSN、密码、密码验证器、原始会话 token 或加密密钥。
- 不要信任未经认证的 Header、Query 或 Body 中的 tenant ID。业务集成应使用可信上游上下文。
- 详细数据结构和迁移示例见 api.md；认证安全细节见 authentication.md。

## 9. 版本与兼容性

该目录描述当前 main 分支已实现的接口，不构成稳定的跨版本兼容承诺。升级前应检查本次版本变更、OpenAPI/代码定义和迁移说明。接口字段以当前 handler 的 JSON 编解码结构为准。
