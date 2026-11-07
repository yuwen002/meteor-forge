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
