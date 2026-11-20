import { describe, expect, it } from 'vitest'
import { tenantHealthRetentionErrorLabel, tenantHealthRetentionStatusLabel, tenantHealthRetentionStatusType } from './tenantHealthRetention'

describe('tenant health retention status presentation', () => {
  it('localizes all persisted cleanup states and maps their tag types', () => {
    expect(tenantHealthRetentionStatusLabel('never_run')).toBe('尚未执行')
    expect(tenantHealthRetentionStatusLabel('succeeded')).toBe('成功')
    expect(tenantHealthRetentionStatusLabel('failed')).toBe('失败')
    expect(tenantHealthRetentionStatusType('never_run')).toBe('info')
    expect(tenantHealthRetentionStatusType('succeeded')).toBe('success')
    expect(tenantHealthRetentionStatusType('failed')).toBe('danger')
  })

  it('does not expose arbitrary error codes in the interface', () => {
    expect(tenantHealthRetentionErrorLabel()).toBe('')
    expect(tenantHealthRetentionErrorLabel('cleanup_failed')).toBe('清理失败')
    expect(tenantHealthRetentionErrorLabel('database-password-leaked')).toBe('未知错误')
  })
})
