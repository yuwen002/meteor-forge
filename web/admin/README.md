# meteor-forge 管理后台前端

## 技术栈

- Vue 3 + TypeScript
- Vite
- Element Plus
- Vue Router
- Axios

## 本地开发

先生成管理元数据加密密钥，并启动 Go 管理 API：

```bash
export METEOR_ADMIN_ENCRYPTION_KEY="$(openssl rand -hex 32)"
go run ./cmd/server
```

Windows PowerShell 可使用：

```powershell
$env:METEOR_ADMIN_ENCRYPTION_KEY = ( -join ((1..32 | ForEach-Object { '{0:x2}' -f (Get-Random -Maximum 256) })) )
go run ./cmd/server
```

默认管理元数据库路径为当前操作系统的用户配置目录下的 `meteor-forge/admin.db`。可通过 `METEOR_ADMIN_DB` 指定路径。**必须妥善备份加密密钥**：更换或丢失密钥后，已保存的数据源 DSN 将无法解密。密钥不要提交到 Git，也不要写入前端环境变量。

API 默认仅监听 `127.0.0.1:8081`。健康检查：

```bash
curl http://127.0.0.1:8081/api/v1/health
```

再启动前端：

```bash
cd web/admin
npm install
npm run dev
```

打开 Vite 输出的本地地址（默认 `http://127.0.0.1:5173`）。开发服务器会将 `/api` 请求代理到 Go API。可通过 `METEOR_ADMIN_API` 修改代理目标。

## 数据源 API

- `GET /api/v1/datasources`：列出数据源，只返回名称、驱动和时间等元数据。
- `POST /api/v1/datasources`：创建数据源，提交 `name`、`driver`、`dsn`。
- `GET /api/v1/datasources/{id}`：获取数据源元信息，不返回 DSN。
- `PUT /api/v1/datasources/{id}`：更新名称、驱动和 DSN；必须提交新的完整 DSN。
- `DELETE /api/v1/datasources/{id}`：删除管理端登记，不会删除目标数据库。
- `POST /api/v1/datasources/{id}/test`：测试目标数据库连接，5 秒超时，错误信息不会回显连接凭据。

支持驱动：MySQL、PostgreSQL、SQLite、ClickHouse。DSN 使用 AES-256-GCM 加密保存。当前管理 API 尚未实现用户认证和角色授权，因此保持 loopback 监听，不要暴露到公网。

## 构建检查

```bash
npm run typecheck
npm run build
```

迁移、租户和分片页面当前仍在后续开发计划中；尚未接入的模块不会显示虚构的业务数据。
