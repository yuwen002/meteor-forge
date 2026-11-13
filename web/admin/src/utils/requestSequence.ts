/**
 * Tracks asynchronous requests so stale responses cannot overwrite newer state.
 */
export function createRequestSequence() {
  let latestRequestID = 0

  return {
    begin(): number {
      latestRequestID += 1
      return latestRequestID
    },
    isCurrent(requestID: number): boolean {
      return requestID === latestRequestID
    },
  }
}

/**
 * Prevents duplicate pagination requests and requests without a continuation cursor.
 */
export function canLoadNextPage(loading: boolean, nextCursor: string): boolean {
  return !loading && nextCursor.length > 0
}


/**
 * Runs an asynchronous request while allowing only its newest invocation to
 * publish data, errors, or loading-state changes.
 */
export async function runLatestRequest<T>(
  sequence: ReturnType<typeof createRequestSequence>,
  request: () => Promise<T>,
  handlers: {
    onSuccess(value: T): void
    onError(error: unknown): void
    onFinally?(): void
  },
): Promise<boolean> {
  const requestID = sequence.begin()
  try {
    const value = await request()
    if (!sequence.isCurrent(requestID)) return false
    handlers.onSuccess(value)
    return true
  } catch (error) {
    if (!sequence.isCurrent(requestID)) return false
    handlers.onError(error)
    return false
  } finally {
    if (sequence.isCurrent(requestID)) handlers.onFinally?.()
  }
}
