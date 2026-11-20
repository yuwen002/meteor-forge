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
  if (range !== '24h' && range !== '7d') throw new Error('无效的趋势范围')
  const pattern = range === '24h'
    ? /^\d{4}-\d{2}-\d{2}T\d{2}:00:00(?:\.0+)?Z$/
    : /^\d{4}-\d{2}-\d{2}T00:00:00(?:\.0+)?Z$/
  if (!pattern.test(bucketStart)) throw new Error('趋势时间桶必须使用 UTC 边界')
  const start = new Date(bucketStart)
  if (
    Number.isNaN(start.getTime())
    || start.toISOString().slice(0, 19) !== bucketStart.slice(0, 19)
  ) {
    throw new Error('无效的趋势时间桶')
  }

  const duration = range === '24h' ? 60 * 60 * 1000 : 24 * 60 * 60 * 1000
  const endExclusiveTime = start.getTime() + duration
  const endExclusive = new Date(endExclusiveTime)
  if (Number.isNaN(endExclusive.getTime())) throw new Error('趋势时间桶超出有效范围')

  const lastSecond = new Date(endExclusiveTime - 1000).toISOString()
  return {
    from: start.toISOString(),
    to: lastSecond.replace(/\.000Z$/, '.999999999Z'),
    pickerRange: [start, new Date(endExclusiveTime - 1)],
  }
}

export function isTenantHealthTrendBucketSelectable(bucket: TenantHealthTrendBucket): boolean {
  return bucket.checks > 0 && bucket.unhealthy > 0
}
