import { describe, expect, it } from 'vitest'
import {
  canSubmitMigrationDisposition,
  canUseMigrationDispositionAction,
  migrationDispositionConfirmation,
} from './migrationDisposition'

const readable = {
  history_available: true,
  current_version_state: 'present' as const,
  current_version_checksum_state: 'matched' as const,
}

describe('迁移任务人工处置交互规则', () => {
  it('只允许管理员对尚未处置的中断任务提交', () => {
    expect(canSubmitMigrationDisposition(true, 'interrupted', null)).toBe(true)
    expect(canSubmitMigrationDisposition(false, 'interrupted', null)).toBe(false)
    expect(canSubmitMigrationDisposition(true, 'running', null)).toBe(false)
    expect(canSubmitMigrationDisposition(true, 'interrupted', { action: 'terminate' })).toBe(false)
  })

  it('目标库历史不可读或核对状态未知时阻止高风险决定', () => {
    expect(canUseMigrationDispositionAction('confirm_completed', { ...readable, history_available: false })).toBe(false)
    expect(canUseMigrationDispositionAction('authorize_retry', { ...readable, current_version_state: 'unknown' })).toBe(false)
    expect(canUseMigrationDispositionAction('confirm_completed', { ...readable, current_version_checksum_state: 'unknown' })).toBe(false)
    expect(canUseMigrationDispositionAction('authorize_retry', { ...readable, current_version_checksum_state: 'mismatch' })).toBe(false)
    expect(canUseMigrationDispositionAction('authorize_retry', { ...readable, current_version_checksum_state: 'local_script_missing' })).toBe(false)
  })

  it('确认完成要求当前版本存在且校验和匹配或为旧版未校验记录', () => {
    expect(canUseMigrationDispositionAction('confirm_completed', readable)).toBe(true)
    expect(canUseMigrationDispositionAction('confirm_completed', { ...readable, current_version_checksum_state: 'legacy_unverified' })).toBe(true)
    expect(canUseMigrationDispositionAction('confirm_completed', { ...readable, current_version_state: 'absent', current_version_checksum_state: 'not_comparable' })).toBe(false)
    expect(canUseMigrationDispositionAction('confirm_completed', { ...readable, current_version_checksum_state: 'mismatch' })).toBe(false)
  })

  it('授权重试要求当前版本不存在且本地脚本可比较', () => {
    expect(canUseMigrationDispositionAction('authorize_retry', { ...readable, current_version_state: 'absent', current_version_checksum_state: 'not_comparable' })).toBe(true)
    expect(canUseMigrationDispositionAction('authorize_retry', readable)).toBe(false)
    expect(canUseMigrationDispositionAction('authorize_retry', { ...readable, current_version_state: 'absent', current_version_checksum_state: 'local_script_missing' })).toBe(false)
  })

  it('允许终止决定记录，但不要求目标库核对可用', () => {
    expect(canUseMigrationDispositionAction('terminate', null)).toBe(true)
    expect(canUseMigrationDispositionAction('terminate', { ...readable, history_available: false })).toBe(true)
  })

  it('要求固定的完整风险确认语句', () => {
    expect(migrationDispositionConfirmation).toBe('我已核对目标数据库实际结构并理解中断迁移风险')
  })
})
