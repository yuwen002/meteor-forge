export type TenantHealthTrendRange = '24h' | '7d'

export interface TenantHealthTrendBucket {
  bucket_start: string
  checks: number
  healthy: number
  unhealthy: number
  success_rate: number
}

export interface TenantHealthTrendHistoryRange {
  from: string
  to: string
  pickerRange: [Date, Date]
}

/**
 * Converts a UTC trend bucket into an inclusive history query range.
 * The API's `to` filter is inclusive, so use the last nanosecond before the
 * next bucket boundary to keep adjacent buckets disjoint.
 */
export function createTenantHealthTrendHistoryRange(
  bucketStart: string,
  range: TenantHealthTrendRange,
): TenantHealthTrendHistoryRange {
  const start = new Date(bucketStart)
  if (Number.isNaN(start.getTime())) throw new Error('无效的趋势时间桶')
  const aligned = range === '24h'
    ? start.getUTCMinutes() === 0 && start.getUTCSeconds() === 0 && start.getUTCMilliseconds() === 0
    : start.getUTCHours() === 0 && start.getUTCMinutes() === 0
      && start.getUTCSeconds() === 0 && start.getUTCMilliseconds() === 0
  if (!aligned) throw new Error('趋势时间桶未对齐 UTC 边界')
  const duration = range === '24h' ? 60 * 60 * 1000 : 24 * 60 * 60 * 1000
  const endExclusive = new Date(start.getTime() + duration)
  const lastSecond = new Date(endExclusive.getTime() - 1000).toISOString()
  return {
    from: start.toISOString(),
    to: lastSecond.replace(/\.000Z$/, '.999999999Z'),
    pickerRange: [start, new Date(endExclusive.getTime() - 1)],
  }
}

export function isTenantHealthTrendBucketSelectable(bucket: TenantHealthTrendBucket): boolean {
  return bucket.checks > 0 && bucket.unhealthy > 0
}
