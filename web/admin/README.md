# meteor-forge 管理后台前端

## 技术栈

- Vue 3 + TypeScript
- Vite
- Element Plus
- Vue Router
- Axios

## 本地开发

先启动 Go 管理 API：

```bash
go run ./cmd/server
```

默认 API 地址为 `http://127.0.0.1:8081`，仅监听本机回环地址。

再启动前端：

```bash
cd web/admin
npm install
npm run dev
```

打开 Vite 输出的本地地址（默认 `http://127.0.0.1:5173`）。开发服务器会将 `/api` 请求代理到 Go API。可通过 `METEOR_ADMIN_API` 修改代理目标。

## 构建检查

```bash
npm run typecheck
npm run build
```

当前版本先建立统一后台布局、导航、服务健康状态和模块空状态。数据源、迁移、租户、分片等页面将在后续阶段接入真实 API；未接入前不显示模拟业务数据。
