import { describe, expect, it } from 'vitest'
import tenantView from '../views/TenantsView.vue?raw'

describe('租户健康趋势交互布局', () => {
  it('在窄屏下支持趋势横向滚动和历史筛选纵向排列', () => {
    expect(tenantView).toContain('.health-trend-chart-scroll { max-width: 100%; overflow-x: auto; }')
    expect(tenantView).toContain('@media (max-width: 460px)')
    expect(tenantView).toContain('.history-toolbar .toolbar-actions { align-items: stretch; flex-direction: column; }')
  })

  it('保留 24 小时与 7 天范围切换入口，并在切换时清空旧图表', () => {
    expect(tenantView).toContain('<el-radio-button label="24h">24 小时</el-radio-button>')
    expect(tenantView).toContain('<el-radio-button label="7d">7 天</el-radio-button>')
    expect(tenantView).toContain('@change="changeHealthTrendRange"')
    expect(tenantView).toContain('healthTrendBuckets.value = []')
    expect(tenantView).toContain('loadLatestTenantHealthTrend')
  })

  it('只为有异常的桶提供可访问的点击入口，并将历史列表设为异常筛选', () => {
    expect(tenantView).toContain(':disabled="!isTenantHealthTrendBucketSelectable(bucket)"')
    expect(tenantView).toContain('@click="selectHealthTrendBucket(bucket)"')
    expect(tenantView).toContain('点击筛选异常历史')
    expect(tenantView).toContain('healthHistoryStatus.value = \'unhealthy\'')
    expect(tenantView).toContain('createTenantHealthHistoryQuery')
  })

  it('为没有匹配历史记录的筛选结果保留明确空状态', () => {
    expect(tenantView).toContain('empty-text="暂无巡检历史"')
    expect(tenantView).toContain('healthHistory.value = next ? [...healthHistory.value, ...data.items] : data.items')
  })
})
