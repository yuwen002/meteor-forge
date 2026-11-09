import { describe, expect, it, vi } from 'vitest'
import { createTenantHealthTrendHistoryRange } from './healthTrend'
import { createTenantHealthHistoryQuery, loadLatestTenantHealthTrend } from './healthTrendInteraction'
import { createRequestSequence } from './requestSequence'

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: Error) => void
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

describe('租户健康趋势请求交互', () => {
  it('切换 24h/7d 时请求对应范围，并忽略较晚返回的旧请求', async () => {
    const sequence = createRequestSequence()
    const first = deferred<{ buckets: string[] }>()
    const second = deferred<{ buckets: string[] }>()
    const requestedRanges: string[] = []
    const appliedBuckets: string[][] = []
    const onError = vi.fn()

    const firstRun = loadLatestTenantHealthTrend(sequence, '24h', async range => {
      requestedRanges.push(range)
      return first.promise
    }, {
      onSuccess: buckets => appliedBuckets.push(buckets),
      onError,
    })
    const secondRun = loadLatestTenantHealthTrend(sequence, '7d', async range => {
      requestedRanges.push(range)
      return second.promise
    }, {
      onSuccess: buckets => appliedBuckets.push(buckets),
      onError,
    })

    second.resolve({ buckets: ['7d-最新结果'] })
    expect(await secondRun).toBe(true)
    first.resolve({ buckets: ['24h-过期结果'] })
    expect(await firstRun).toBe(false)

    expect(requestedRanges).toEqual(['24h', '7d'])
    expect(appliedBuckets).toEqual([['7d-最新结果']])
    expect(onError).not.toHaveBeenCalled()
  })

  it('请求失败时通知界面清空旧趋势，并结束加载状态', async () => {
    const sequence = createRequestSequence()
    let buckets = ['旧范围数据']
    let loading = true
    const onError = vi.fn(() => { buckets = [] })
    const onFinally = vi.fn(() => { loading = false })

    const applied = await loadLatestTenantHealthTrend(sequence, '7d', async () => {
      throw new Error('network unavailable')
    }, {
      onSuccess: value => { buckets = value },
      onError,
      onFinally,
    })

    expect(applied).toBe(false)
    expect(buckets).toEqual([])
    expect(onError).toHaveBeenCalledOnce()
    expect(onFinally).toHaveBeenCalledOnce()
    expect(loading).toBe(false)
  })

  it('空响应会发布空桶数组，而不是保留上一次趋势', async () => {
    const sequence = createRequestSequence()
    let buckets = ['旧数据']

    const applied = await loadLatestTenantHealthTrend(sequence, '24h', async () => ({
      buckets: [],
    }), {
      onSuccess: value => { buckets = value },
      onError: () => { throw new Error('不应进入失败回调') },
    })

    expect(applied).toBe(true)
    expect(buckets).toEqual([])
  })

  it('趋势桶点击后的历史查询使用 UTC 左闭右开边界和异常状态', () => {
    const range = createTenantHealthTrendHistoryRange('2026-11-08T12:00:00Z', '24h')
    const query = createTenantHealthHistoryQuery({
      status: 'unhealthy',
      source: 'scheduled',
      tenantID: 'tenant-1',
      timeRange: range.pickerRange,
      exactTo: range.to,
      cursor: 'opaque-cursor',
    })

    expect(query).toEqual({
      limit: 20,
      status: 'unhealthy',
      source: 'scheduled',
      tenant_id: 'tenant-1',
      from: '2026-11-08T12:00:00.000Z',
      to: '2026-11-08T12:59:59.999999999Z',
      cursor: 'opaque-cursor',
    })
  })

  it('未设置时间范围时不发送残留的 from/to', () => {
    expect(createTenantHealthHistoryQuery({
      exactTo: '2026-11-08T12:59:59.999999999Z',
    })).toEqual({ limit: 20 })
  })
})
