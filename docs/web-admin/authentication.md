# Web 管理后台认证与授权

> 当前状态：已实现首次管理员初始化、密码校验、持久化会话、登录/登出/密码修改接口、后端 RBAC、管理员账号管理、前端登录与角色路由保护，以及操作/安全审计。管理 API 强制 loopback 监听。该服务不应暴露到公网。

## 1. 安全目标与边界

管理 API 可以登记数据库连接、执行迁移、回滚 schema、调整租户映射并创建分片物理表。loopback 监听是纵深防御，不是认证的替代品。

默认地址为 `127.0.0.1:8081`。当前只接受 `localhost`、`127.0.0.1` 或 `::1`。不要把服务暴露到公网，也不要在没有可信代理、TLS、Cookie 和审计设计的情况下放宽监听限制。

除明确实现的请求 Origin 校验外，不信任 `X-Forwarded-For` 等转发头作为身份、审计来源或登录限流依据。

## 2. 首次管理员初始化

- `METEOR_ADMIN_USERNAME`：可选，默认 `admin`。
- `METEOR_ADMIN_BOOTSTRAP_PASSWORD`：仅在管理元数据库尚无管理员时必需，至少 12 个字符。
- 首次启动成功后，管理员凭据持久化保存；后续重启不会使用环境变量重置已有密码。
- 如果没有管理员且未设置初始密码，服务拒绝启动，不会启动无认证的管理 API。
- 不提供公开注册或公开密码重置接口。

## 3. 密码存储

当前使用标准库 PBKDF2-HMAC-SHA256，迭代次数 310,000，每个密码使用独立的 16 字节随机盐，派生 32 字节密钥，并使用版本化编码和恒定时间比较。该实现避免新增依赖，但与 Argon2id 相比属于有意识的取舍；在扩大部署范围前应重新评估工作因子和算法选择。

不得使用明文密码、可逆加密、快速无盐哈希保存密码。API 不返回密码验证器。

## 4. 会话机制

- 会话 Token 使用密码学安全随机源生成，至少 256 bit。
- 原始 Token 只通过 Cookie 返回；管理元数据库只保存 Token 的 SHA-256 摘要。
- 会话包含用户名、创建时间、过期时间和撤销状态。
- 当前会话有效期为 12 小时。
- Cookie 使用 HttpOnly、SameSite=Strict、Path=/api/v1；HTTPS 场景设置 Secure。
- 退出登录会在服务端撤销当前会话并清理 Cookie。
- 修改密码和管理员禁用/删除等操作会撤销相关活动会话；角色变更会使已有会话失效。
- 过期会话即使尚未清理也必须被拒绝。

## 5. HTTP 认证接口

| 方法 | 路径 | 访问控制 | 说明 |
|---|---|---|---|
| POST | `/api/v1/auth/login` | 公开、限流 | 校验账号密码并签发会话 Cookie |
| GET | `/api/v1/auth/session` | 已登录 | 返回当前用户名、角色和过期时间 |
| POST | `/api/v1/auth/logout` | 已登录 + Origin/CSRF 校验 | 撤销当前会话 |
| POST | `/api/v1/auth/password` | 已登录 + Origin/CSRF 校验 | 校验当前密码后修改密码 |
| GET | `/api/v1/auth/users` | administrator | 列出管理员账号，不返回密码验证器 |
| POST | `/api/v1/auth/users` | administrator | 创建管理员账号 |
| PUT | `/api/v1/auth/users/{id}/role` | administrator | 修改角色 |
| DELETE | `/api/v1/auth/users/{id}` | administrator | 删除账号；不能删除最后一个管理员 |
| GET | `/api/v1/auth/security-events` | administrator | 查询安全审计事件 |
| GET | `/api/v1/health` | 公开 | 最小存活响应 |

所有其他 `/api/v1/**` 管理接口均要求有效会话。未知 API 路由同样处于认证中间件保护下。

登录请求使用 JSON 提交 username/password。失败响应使用通用认证错误，避免枚举用户名。响应不得包含密码验证器、会话 Token 或 bootstrap 密码。

## 6. Cookie、CSRF 与浏览器保护

- 使用 host-only Cookie，不设置 Domain；HttpOnly、SameSite=Strict，并限制 API Path。
- 对 Cookie 认证的状态变更请求校验 Origin 与请求 Host；拒绝跨源请求，不启用通配 CORS。
- 认证状态变更接口要求 JSON Content-Type。
- 响应设置 no-store、nosniff、no-referrer 等安全头。
- 不得将会话 Token 放入 localStorage、URL、日志、分析平台或前端业务状态。
- 如未来确需跨源管理，应单独设计 CSRF Token 和可信 Origin 模型，不应简单放宽现有策略。

## 7. 登录限流

当前使用有界内存限流器，按直接对端 IP 和“对端 IP + 规范化用户名”分别计数。每个桶允许 5 次尝试，之后冷却 30 秒；冷却后重新计数。最多跟踪 4,096 个 key，并清理过期记录。不信任转发头。

该限流器不是跨实例共享的；未来若支持多实例部署，需要迁移到共享存储，并重新设计可信代理边界。

## 8. RBAC 角色

- `viewer`：读取数据源、迁移状态、租户、分片规则、任务详情和操作审计。
- `migration_operator`：viewer 权限，加迁移预览和 Up 执行；回滚仍仅管理员可执行。
- `shard_operator`：viewer 权限，加分片结构克隆预览和 DDL 执行。
- `administrator`：全部管理权限，包括数据源/租户/规则配置、回滚、账号和安全审计管理。

权限由后端中间件和 Handler 双重执行，不能只依赖前端按钮隐藏。迁移操作员只能取消 Up 任务；回滚和版本变更任务的取消仅管理员可执行。完整矩阵见 [角色权限矩阵](permissions.md)。

## 9. 安全审计

管理员专属页面「系统管理 → 安全审计」读取 `GET /api/v1/auth/security-events`。事件覆盖登录成功/失败、登出、密码修改、管理员创建、角色变更和删除。

事件包含 actor、subject、action、outcome、直接对端地址、简短静态详情和 UTC 时间戳。成功的管理员变更与安全审计事件在同一管理元数据库事务中提交；审计写入失败时关联变更回滚。事件不得包含提交的密码、Cookie、Token、DSN 或驱动原始错误。

## 10. 运维注意事项

- 管理元数据库加密密钥必须单独安全保管；丢失密钥后，已保存 DSN 无法解密。
- 定期备份管理元数据库并演练恢复；该备份不包含目标业务数据库。
- 管理 API 维持 loopback 监听；不要通过公网或未受信任的反向代理暴露。
- 当前登录限流仅为单进程内存状态，不应把它描述为多实例防护。
- 对登录失败、角色变更、管理员删除和异常任务状态定期检查安全审计。

相关文档：[部署与运维手册](deployment.md)、[API 文档](api.md)、[备份与恢复](backup-restore.md)。
