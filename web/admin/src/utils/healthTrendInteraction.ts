import { createRequestSequence, runLatestRequest } from './requestSequence'
import type { TenantHealthTrendRange } from './healthTrend'

type RequestSequence = ReturnType<typeof createRequestSequence>

export interface TenantHealthTrendResponse<T> {
  buckets?: T[] | null
}

export interface TenantHealthHistoryQueryInput {
  source?: string
  status?: string
  tenantID?: string
  timeRange?: [Date, Date] | null
  exactTo?: string
  cursor?: string
}

export interface TenantHealthHistoryQuery {
  limit: number
  source?: string
  status?: string
  tenant_id?: string
  from?: string
  to?: string
  cursor?: string
}

/**
 * Loads one trend range and lets only the newest range request publish state.
 * Missing bucket arrays are normalized to an empty series.
 */
export function loadLatestTenantHealthTrend<T>(
  sequence: RequestSequence,
  range: TenantHealthTrendRange,
  request: (range: TenantHealthTrendRange) => Promise<TenantHealthTrendResponse<T>>,
  handlers: {
    onSuccess(buckets: T[]): void
    onError(error: unknown): void
    onFinally?(): void
  },
): Promise<boolean> {
  return runLatestRequest(sequence, () => request(range), {
    onSuccess: response => handlers.onSuccess(response.buckets ?? []),
    onError: handlers.onError,
    onFinally: handlers.onFinally,
  })
}

/**
 * Builds the history API query from the visible filters. Trend-selected ranges
 * keep their nanosecond-precise inclusive end instead of the date picker's
 * millisecond-only value.
 */
export function createTenantHealthHistoryQuery(
  input: TenantHealthHistoryQueryInput,
): TenantHealthHistoryQuery {
  const query: TenantHealthHistoryQuery = { limit: 20 }
  if (input.source) query.source = input.source
  if (input.status) query.status = input.status
  if (input.tenantID) query.tenant_id = input.tenantID
  if (input.timeRange) {
    query.from = input.timeRange[0].toISOString()
    query.to = input.exactTo || input.timeRange[1].toISOString()
  }
  if (input.cursor) query.cursor = input.cursor
  return query
}
