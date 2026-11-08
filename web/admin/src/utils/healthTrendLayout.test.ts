import { describe, expect, it } from 'vitest'
import tenantView from '../views/TenantsView.vue?raw'

describe('租户健康趋势交互布局', () => {
  it('在窄屏下支持趋势横向滚动和历史筛选纵向排列', () => {
    expect(tenantView).toContain('.health-trend-chart-scroll { max-width: 100%; overflow-x: auto; }')
    expect(tenantView).toContain('@media (max-width: 460px)')
    expect(tenantView).toContain('.history-toolbar .toolbar-actions { align-items: stretch; flex-direction: column; }')
  })
  it('只为有异常的桶提供可访问的点击入口', () => {
    expect(tenantView).toContain(':disabled="!isTenantHealthTrendBucketSelectable(bucket)"')
    expect(tenantView).toContain('@click="selectHealthTrendBucket(bucket)"')
    expect(tenantView).toContain('点击筛选异常历史')
  })
})
