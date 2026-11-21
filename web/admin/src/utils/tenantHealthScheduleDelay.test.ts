import { describe, expect, it } from 'vitest'
import tenantView from '../views/TenantsView.vue?raw'

describe('租户巡检调度延迟趋势展示', () => {
  it('展示最近 10 轮样本及统计指标', () => {
    expect(tenantView).toContain('recent_schedule_delays_ms: number[]')
    expect(tenantView).toContain('average_schedule_delay_ms: number')
    expect(tenantView).toContain('max_schedule_delay_ms: number')
    expect(tenantView).toContain('delayed_schedule_count: number')
    expect(tenantView).toContain('近 10 轮平均 / 最大延迟')
    expect(tenantView).toContain('近 10 轮延迟超 30 秒')
    expect(tenantView).toContain('最近调度延迟趋势（ms）')
  })

  it('空样本显示明确的空状态', () => {
    expect(tenantView).toContain('v-if="!healthScheduler.recent_schedule_delays_ms.length"')
    expect(tenantView).toContain('暂无样本')
  })

  it('仅将严格超过 30 秒的样本标记为异常', () => {
    expect(tenantView).toContain(":type=\"delay > 30000 ? 'danger' : 'info'\"")
  })

  it('为窄屏保留自适应的调度状态网格', () => {
    expect(tenantView).toContain('.health-scheduler-overview { grid-template-columns: 1fr; }')
    expect(tenantView).toContain('@media (max-width: 460px)')
  })
})
