import type { TenantHealthSchedulerRunStatus, TenantHealthSchedulerRunSummary } from '../api'

export interface TenantHealthSchedulerRunQueryInput {
  status: TenantHealthSchedulerRunStatus | ''
  timeRange: [Date, Date] | null
  page: number
  pageSize: number
}

export function createTenantHealthSchedulerRunQuery(input: TenantHealthSchedulerRunQueryInput) {
  const params: {
    status?: TenantHealthSchedulerRunStatus
    from?: string
    to?: string
    limit: number
    offset: number
  } = {
    limit: input.pageSize,
    offset: Math.max(0, input.page - 1) * input.pageSize,
  }
  if (input.status) params.status = input.status
  if (input.timeRange) {
    params.from = input.timeRange[0].toISOString()
    params.to = input.timeRange[1].toISOString()
  }
  return params
}

export function tenantHealthSchedulerRunSummaryMetrics(summary: TenantHealthSchedulerRunSummary) {
  return [
    { key: 'total', label: '总轮次', value: String(summary.total) },
    { key: 'running', label: '执行中', value: String(summary.running) },
    { key: 'succeeded', label: '成功', value: String(summary.succeeded) },
    { key: 'failed', label: '失败', value: String(summary.failed) },
    { key: 'lease_lost', label: '租约丢失', value: String(summary.lease_lost) },
    { key: 'interrupted', label: '接管中断', value: String(summary.interrupted) },
    { key: 'average_duration', label: '平均耗时', value: tenantHealthSchedulerRunDuration(summary.average_duration_ms) },
  ]
}

export function tenantHealthSchedulerRunStatusLabel(status: TenantHealthSchedulerRunStatus): string {
  const labels: Record<TenantHealthSchedulerRunStatus, string> = {
    running: '执行中',
    succeeded: '成功',
    failed: '失败',
    lease_lost: '租约丢失',
    interrupted: '接管后中断',
  }
  return labels[status]
}

export function tenantHealthSchedulerRunStatusType(status: TenantHealthSchedulerRunStatus): 'success' | 'danger' | 'warning' | 'info' | 'primary' {
  if (status === 'succeeded') return 'success'
  if (status === 'failed' || status === 'lease_lost') return 'danger'
  if (status === 'interrupted') return 'warning'
  return 'primary'
}

export function tenantHealthSchedulerRunDuration(durationMS: number): string {
  if (durationMS < 1000) return durationMS + ' ms'
  return (durationMS / 1000).toFixed(2) + ' 秒'
}
