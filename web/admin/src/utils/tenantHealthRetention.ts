import type { TenantHealthRetentionCleanup } from '../api'

export type TenantHealthRetentionStatus = TenantHealthRetentionCleanup['status']
export type TenantHealthRetentionTagType = 'success' | 'danger' | 'info'

export function tenantHealthRetentionStatusLabel(status: TenantHealthRetentionStatus): string {
  switch (status) {
    case 'succeeded': return '成功'
    case 'failed': return '失败'
    case 'never_run': return '尚未执行'
  }
}

export function tenantHealthRetentionStatusType(status: TenantHealthRetentionStatus): TenantHealthRetentionTagType {
  switch (status) {
    case 'succeeded': return 'success'
    case 'failed': return 'danger'
    case 'never_run': return 'info'
  }
}

export function tenantHealthRetentionErrorLabel(errorCode?: string): string {
  if (!errorCode) return ''
  return errorCode === 'cleanup_failed' ? '清理失败' : '未知错误'
}
