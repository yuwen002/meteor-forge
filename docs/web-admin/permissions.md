# Web 管理后台角色权限矩阵

所有管理 API（健康检查和登录入口除外）均要求有效会话。权限由 Go 后端中间件和具体 Handler 强制执行；前端隐藏按钮仅用于改善体验，不构成安全边界。

## 角色定义

| 角色 | 定位 |
|---|---|
| `viewer` | 只读查看者 |
| `migration_operator` | 迁移操作员 |
| `shard_operator` | 分片操作员 |
| `administrator` | 管理员，拥有全部管理权限 |

## 权限矩阵

| 操作 | viewer | migration_operator | shard_operator | administrator |
|---|---:|---:|---:|---:|
| 查看数据源、租户、迁移状态/任务/任务审计、分片规则/物理表、操作审计 | ✓ | ✓ | ✓ | ✓ |
| 查看当前会话 | ✓ | ✓ | ✓ | ✓ |
| 查看租户最近连接巡检状态 | ✓ | ✓ | ✓ | ✓ |
| 触发单租户/批量连接巡检 | — | — | — | ✓ |
| 修改本人密码、退出登录 | ✓ | ✓ | ✓ | ✓ |
| 创建/更新/删除数据源 | — | — | — | ✓ |
| 测试数据源或租户连接 | — | — | — | ✓ |
| 创建/更新/删除租户 | — | — | — | ✓ |
| 租户发现预览与确认导入 | — | — | — | ✓ |
| 创建/更新/删除分片规则 | — | — | — | ✓ |
| 迁移预览、Up、租户批量 Up | — | ✓ | — | ✓ |
| Down、DownSteps、ToVersion | — | — | — | ✓ |
| 取消 Up 迁移任务 | — | ✓ | — | ✓ |
| 取消回滚/版本变更任务 | — | — | — | ✓ |
| 分片路由预览、物理表存在性检查 | ✓ | — | ✓ | ✓ |
| 分片结构克隆预览、提交和取消 DDL | — | — | ✓ | ✓ |
| 管理员账号与角色管理 | — | — | — | ✓ |
| 查看安全审计事件 | — | — | — | ✓ |

## 权限使用注意

- `migration_operator` 可提交 Up 和批量租户 Up；不能提交回滚或版本跳转。
- 通用迁移任务取消接口会先读取任务动作，再决定权限；迁移操作员不能借取消接口取消管理员专属的回滚任务。
- `shard_operator` 可提交结构克隆 DDL，但不能管理数据源、租户或分片规则配置。
- 所有已认证角色可读取操作审计和租户生命周期审计；安全审计仅管理员可读。
- 租户发现导入、管理员管理、账号角色变更和安全审计均为管理员权限。
- 回滚和分片结构克隆必须显式确认。前端二次确认不能替代服务端确认字段和角色检查。
- 未列出的写操作默认拒绝。不要假设“页面上看得到”就代表接口可写。

## 认证相关接口

| 接口 | 权限 |
|---|---|
| `POST /api/v1/auth/login` | 公开，带登录限流 |
| `GET /api/v1/auth/session` | 已登录 |
| `POST /api/v1/auth/logout` | 已登录，Origin/CSRF 检查 |
| `POST /api/v1/auth/password` | 已登录，验证当前密码并执行 Origin/CSRF 检查 |
| `GET /api/v1/auth/users` | administrator |
| `POST /api/v1/auth/users` | administrator |
| `PUT /api/v1/auth/users/{id}/role` | administrator |
| `DELETE /api/v1/auth/users/{id}` | administrator；不能删除最后一个管理员 |
| `GET /api/v1/auth/security-events` | administrator |
| `GET /api/v1/health` | 公开，只返回最小存活信息 |

完整接口定义见 [API 文档](../api/README.md)，认证机制见 [认证与安全说明](authentication.md)。
