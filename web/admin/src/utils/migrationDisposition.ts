export type MigrationDispositionAction = 'confirm_completed' | 'authorize_retry' | 'terminate'

export const migrationDispositionConfirmation = '我已核对目标数据库实际结构并理解中断迁移风险'

export const migrationDispositionLabels: Record<MigrationDispositionAction, string> = {
  confirm_completed: '确认已完成',
  authorize_retry: '授权后续重新提交',
  terminate: '终止并保留现场',
}

export interface MigrationDispositionReconciliation {
  history_available: boolean
  current_version_state: 'present' | 'absent' | 'unknown'
  current_version_checksum_state: 'matched' | 'mismatch' | 'legacy_unverified' | 'local_script_missing' | 'not_comparable' | 'unknown'
}

export function canSubmitMigrationDisposition(
  isAdministrator: boolean,
  taskStatus: string,
  existingDisposition: unknown,
): boolean {
  return isAdministrator && taskStatus === 'interrupted' && existingDisposition == null
}

export function canUseMigrationDispositionAction(
  action: MigrationDispositionAction,
  reconciliation: MigrationDispositionReconciliation | null,
): boolean {
  if (action === 'terminate') return true
  if (!reconciliation || !reconciliation.history_available) return false
  if (action === 'confirm_completed') {
    return reconciliation.current_version_state === 'present' &&
      (reconciliation.current_version_checksum_state === 'matched' ||
        reconciliation.current_version_checksum_state === 'legacy_unverified')
  }
  return reconciliation.current_version_state === 'absent' &&
    reconciliation.current_version_checksum_state === 'not_comparable'
}
