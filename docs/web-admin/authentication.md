# 管理后台认证与授权

## 1. 当前安全边界

管理 API 默认监听 127.0.0.1:8081，并且启动时拒绝非 loopback 地址。认证不是允许公网访问的理由；当前实现不支持受信任反向代理、远程管理或公网部署。不要通过代理或端口映射绕过 loopback 限制。

健康检查 GET /api/v1/health 公开，但只返回最小存活信息。其他管理 API 均要求有效会话。

## 2. 首次管理员初始化

首次启动时：

- METEOR_ADMIN_USERNAME 可选，默认 admin。
- METEOR_ADMIN_BOOTSTRAP_PASSWORD 在尚无管理员时必需，至少 12 个字符。
- METEOR_ADMIN_ENCRYPTION_KEY 必需，为 64 位十六进制字符串，解码后 32 字节。
- 管理员创建成功后，启动环境变量不会重置已有密码；不存在公开注册或密码重置接口。

生成密钥：

~~~sh
openssl rand -hex 32
~~~

请通过安全配置渠道注入密码和密钥，不要提交到 Git、前端环境变量或日志。

## 3. 密码存储

当前实现使用标准库 PBKDF2-HMAC-SHA256，310,000 次迭代、每个密码独立的 16 字节随机盐、32 字节派生密钥和常量时间比较。数据库只保存带版本信息的密码验证器，不保存明文密码。

该方案是当前实现的明确取舍，不等同于已采用 Argon2id。部署范围扩大前应重新评估密码哈希参数及算法升级计划。

## 4. 会话机制

- 登录接口 POST /api/v1/auth/login 校验账号密码并签发会话。
- 会话 token 至少 256 bit 随机；原始 token 只通过 Cookie 返回，管理元数据库只保存 SHA-256 摘要。
- Cookie 名称为 meteor_admin_session，HttpOnly、SameSite=Strict、Path=/api/v1，生命周期为 12 小时；HTTPS 场景设置 Secure。
- GET /api/v1/auth/session 返回当前管理员身份和过期时间。
- 登出会撤销服务端会话并清除 Cookie。
- 修改密码、角色变更和账号删除会撤销相关现有会话。
- 过期会话即使尚未清理，也会被拒绝。

不要将 Cookie/token 放进 localStorage、URL、日志、埋点或前端应用状态。

## 5. CSRF 与浏览器请求保护

- 对 Cookie 认证的状态变更请求校验 Origin 与请求 Host。
- 拒绝跨源状态变更请求，不启用通配符 CORS。
- 认证状态变更接口要求 JSON Content-Type。
- 设置 no-store、nosniff 和 no-referrer 等响应头。
- 不信任 X-Forwarded-For 等转发头作为身份或来源依据，除非未来实现显式可信代理配置。

当前前端通过 Vite 同源代理访问本地 API。自定义开发代理时应保持浏览器 Host/Origin 语义，不要随意更改跨源策略。

## 6. 登录限流与错误信息

登录接口具有服务端限流。用户名或密码错误使用通用认证失败响应，避免枚举用户名。响应不得包含密码验证器、会话 token 或 bootstrap secret。

## 7. 角色与权限

| 角色 | 权限范围 |
|---|---|
| administrator | 完整管理权限，包括账号、数据源、租户、分片规则、回滚和 DDL |
| migration_operator | 查看资源；提交获准的 Up 迁移和取消迁移任务；不能回滚 |
| shard_operator | 查看资源；预览分片路由；提交物理表结构克隆和取消 DDL |
| viewer | 只读 |

后端中间件对每个 API 请求执行认证和角色授权。前端路由守卫和按钮显隐只负责用户体验，不能作为安全边界。

## 8. 管理员账号管理

管理员专属接口：

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | /api/v1/auth/users | 列出账号，不返回密码验证器 |
| POST | /api/v1/auth/users | 创建账号 |
| PUT | /api/v1/auth/users/{id}/role | 修改角色 |
| DELETE | /api/v1/auth/users/{id} | 删除账号 |
| GET | /api/v1/auth/security-events | 查看安全审计 |

密码至少 12 个字符。不能修改自己的角色；不能删除或降级最后一个管理员。账号角色变更或删除会撤销该账号的会话。

## 9. 安全审计

安全审计事件覆盖登录成功/失败、登出、密码变更、管理员创建、角色变更和删除。仅 administrator 可读取。记录包含操作者、目标账号、结果、请求来源和说明，不包含密码或会话凭据。

请求来源使用管理 API 直接观察到的远端地址，不信任未经配置的转发头。

## 10. 部署注意事项

- 保持 loopback-only 监听。
- 加密密钥必须安全备份；丢失密钥后，已保存 DSN 无法解密。
- 管理元数据库文件和父目录应限制访问。
- 不要把管理 API 暴露到公网。
- 远程访问、TLS 终止、可信代理、MFA、审计完整性保护和多节点协调均不属于当前已交付能力。
