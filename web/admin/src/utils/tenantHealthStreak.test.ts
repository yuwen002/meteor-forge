import { describe, expect, it } from 'vitest'
import tenantView from '../views/TenantsView.vue?raw'

describe('租户连续失败诊断展示', () => {
  it('租户健康接口字段与列表展示保持一致', () => {
    expect(tenantView).toContain('consecutive_failures: number')
    expect(tenantView).toContain('last_success_at: string | null')
    expect(tenantView).toContain('连续失败（保留范围）')
    expect(tenantView).toContain('最近成功')
    expect(tenantView).toContain('healthByTenant[row.id].consecutive_failures')
    expect(tenantView).toContain('tenantHealthLastSuccessLabel(healthByTenant[row.id])')
    expect(tenantView).toContain('保留范围内无成功记录')
  })
})
