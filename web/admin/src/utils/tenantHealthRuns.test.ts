import { describe, expect, it } from 'vitest'
import { createRequestSequence, runLatestRequest } from './requestSequence'
import { createTenantHealthSchedulerRunQuery, tenantHealthSchedulerRunDuration, tenantHealthSchedulerRunStatusLabel, tenantHealthSchedulerRunStatusType } from './tenantHealthRuns'

describe('tenant health scheduler run request sequence', () => {
  it('publishes only the latest filter request', async () => {
    const sequence = createRequestSequence()
    let resolveOld!: (value: string) => void
    const oldRequest = new Promise<string>(resolve => { resolveOld = resolve })
    const values: string[] = []
    const oldRun = runLatestRequest(sequence, () => oldRequest, {
      onSuccess: value => values.push(value),
      onError: () => values.push('old-error'),
    })
    await runLatestRequest(sequence, async () => 'new-filter', {
      onSuccess: value => values.push(value),
      onError: () => values.push('new-error'),
    })
    resolveOld('stale-filter')
    await oldRun
    expect(values).toEqual(['new-filter'])
  })

  it('does not publish a stale failure after a newer request succeeds', async () => {
    const sequence = createRequestSequence()
    let rejectOld!: (reason?: unknown) => void
    const oldRequest = new Promise<string>((_resolve, reject) => { rejectOld = reject })
    const errors: string[] = []
    const oldRun = runLatestRequest(sequence, () => oldRequest, {
      onSuccess: () => undefined,
      onError: () => errors.push('stale-error'),
    })
    await runLatestRequest(sequence, async () => 'ok', {
      onSuccess: () => undefined,
      onError: () => errors.push('new-error'),
    })
    rejectOld(new Error('network failure'))
    await oldRun
    expect(errors).toEqual([])
  })
})

describe('tenant health scheduler run filters and presentation', () => {
  it('encodes status, inclusive time bounds, and pagination', () => {
    const from = new Date('2026-11-13T01:00:00.000Z')
    const to = new Date('2026-11-13T02:00:00.000Z')
    expect(createTenantHealthSchedulerRunQuery({
      status: 'failed', timeRange: [from, to], page: 3, pageSize: 20,
    })).toEqual({
      status: 'failed',
      from: '2026-11-13T01:00:00.000Z',
      to: '2026-11-13T02:00:00.000Z',
      limit: 20,
      offset: 40,
    })
  })

  it('omits optional filters and keeps the first page offset at zero', () => {
    expect(createTenantHealthSchedulerRunQuery({
      status: '', timeRange: null, page: 1, pageSize: 20,
    })).toEqual({ limit: 20, offset: 0 })
  })

  it('localizes every run status and formats duration', () => {
    expect(tenantHealthSchedulerRunStatusLabel('lease_lost')).toBe('租约丢失')
    expect(tenantHealthSchedulerRunStatusLabel('interrupted')).toBe('接管后中断')
    expect(tenantHealthSchedulerRunStatusType('failed')).toBe('danger')
    expect(tenantHealthSchedulerRunStatusType('succeeded')).toBe('success')
    expect(tenantHealthSchedulerRunDuration(450)).toBe('450 ms')
    expect(tenantHealthSchedulerRunDuration(1250)).toBe('1.25 秒')
  })
})
