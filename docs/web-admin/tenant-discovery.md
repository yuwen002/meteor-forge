# 动态租户发现与同步

## 当前实现范围

动态租户发现通过已登记的数据源连接控制库，读取约定的 `t_tenants` 表：

- 必需列：`tenant_id`、`dsn`、`status`。
- 可选列：`driver`。未提供时由管理员选择目标数据源。
- 只读取 `status = 'active'` 的记录。
- 查询超时为 10 秒；控制库凭据保存在管理元数据库中，接口不会返回 DSN。

## 预览

管理员调用：

`GET /api/v1/tenant-discovery/preview?data_source_id=<control-data-source-id>`

响应只包含控制库租户 ID、可选驱动、建议名称/标识及本地是否已存在同标识租户。不会返回 DSN，也不会修改任何数据。

## 确认导入

管理员必须在预览后明确提交：

`POST /api/v1/tenant-discovery/sync`

请求体示例：

```json
{
  "control_data_source_id": "control-source-id",
  "confirm": true,
  "items": [
    {
      "control_tenant_id": "tenant-a",
      "name": "Tenant A",
      "code": "tenant-a",
      "data_source_id": "registered-target-source-id"
    }
  ]
}
```

服务端会重新读取控制库，确认所选租户仍处于 active 状态，并验证每个目标数据源已登记；若控制库提供 driver，还会校验目标数据源驱动一致。导入批次在管理元数据库中原子提交，并为每个新租户写入生命周期审计记录。

## 安全边界

- 仅管理员可以预览和执行同步。
- 控制库中的 DSN 仅用于发现查询，绝不回传 API 或写入日志。
- 租户目标数据源必须预先登记并加密保存；发现流程不会自动导入外部 DSN/凭据。
- 同步只创建本地租户；遇到重复标识时整批回滚，不覆盖现有租户。
- 不自动删除、不自动停用、不自动修改已有租户。
- 同步请求必须显式设置 `confirm: true`，且最多 500 条。
- 控制库结构为约定格式；结构不同应先做显式适配，不猜测表名或字段。

## 后续工作

下一步补充前端预览与目标数据源映射交互、刷新/过期提示，以及连接健康巡检与最近巡检状态。
