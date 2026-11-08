import { describe, expect, it } from 'vitest'
import { createTenantHealthTrendHistoryRange, isTenantHealthTrendBucketSelectable } from './healthTrend'

describe('createTenantHealthTrendHistoryRange', () => {
  it('uses an exact UTC hour and excludes the next hour boundary', () => {
    const result = createTenantHealthTrendHistoryRange('2026-11-08T12:00:00Z', '24h')
    expect(result.from).toBe('2026-11-08T12:00:00.000Z')
    expect(result.to).toBe('2026-11-08T12:59:59.999999999Z')
    expect(result.pickerRange[1].toISOString()).toBe('2026-11-08T12:59:59.999Z')
  })
  it('uses an exact UTC calendar day and excludes the next day boundary', () => {
    const result = createTenantHealthTrendHistoryRange('2026-11-08T00:00:00Z', '7d')
    expect(result.from).toBe('2026-11-08T00:00:00.000Z')
    expect(result.to).toBe('2026-11-08T23:59:59.999999999Z')
  })
  it('rejects invalid or non-aligned timestamps', () => {
    expect(() => createTenantHealthTrendHistoryRange('bad-date', '24h')).toThrow('无效')
    expect(() => createTenantHealthTrendHistoryRange('2026-11-08T12:30:00Z', '24h')).toThrow('UTC')
    expect(() => createTenantHealthTrendHistoryRange('2026-11-08T01:00:00Z', '7d')).toThrow('UTC')
  })
})

describe('isTenantHealthTrendBucketSelectable', () => {
  it('allows only buckets with abnormal checks', () => {
    expect(isTenantHealthTrendBucketSelectable({bucket_start:'2026-11-08T12:00:00Z',checks:2,healthy:1,unhealthy:1,success_rate:50})).toBe(true)
    expect(isTenantHealthTrendBucketSelectable({bucket_start:'2026-11-08T13:00:00Z',checks:0,healthy:0,unhealthy:0,success_rate:0})).toBe(false)
    expect(isTenantHealthTrendBucketSelectable({bucket_start:'2026-11-08T14:00:00Z',checks:3,healthy:3,unhealthy:0,success_rate:100})).toBe(false)
  })
})
