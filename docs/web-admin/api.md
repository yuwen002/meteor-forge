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
