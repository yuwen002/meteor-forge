import { describe, expect, it } from 'vitest'
import { canLoadNextPage, createRequestSequence, runLatestRequest } from './requestSequence'

describe('createRequestSequence', () => {
  it('keeps only the newest request eligible to update state', () => {
    const requests = createRequestSequence()
    const first = requests.begin()
    const second = requests.begin()

    expect(requests.isCurrent(first)).toBe(false)
    expect(requests.isCurrent(second)).toBe(true)
  })

  it('keeps the current request valid until another request begins', () => {
    const requests = createRequestSequence()
    const requestID = requests.begin()

    expect(requests.isCurrent(requestID)).toBe(true)
    expect(requests.begin()).toBe(requestID + 1)
    expect(requests.isCurrent(requestID)).toBe(false)
  })
})

describe('canLoadNextPage', () => {
  it('allows pagination only when idle and a cursor exists', () => {
    expect(canLoadNextPage(false, 'next-page')).toBe(true)
    expect(canLoadNextPage(true, 'next-page')).toBe(false)
    expect(canLoadNextPage(false, '')).toBe(false)
  })
})


describe('runLatestRequest', () => {
  it('publishes empty data for the empty state', async () => {
    const sequence = createRequestSequence()
    let received: string[] | undefined
    let finished = false
    const applied = await runLatestRequest(sequence, async () => [], {
      onSuccess: (value: string[]) => { received = value },
      onError: () => { throw new Error('unexpected failure') },
      onFinally: () => { finished = true },
    })
    expect(applied).toBe(true)
    expect(received).toEqual([])
    expect(finished).toBe(true)
  })

  it('reports request failures and clears loading state', async () => {
    const sequence = createRequestSequence()
    const failure = new Error('network unavailable')
    let receivedError: unknown
    let finished = false
    const applied = await runLatestRequest(sequence, async () => { throw failure }, {
      onSuccess: () => { throw new Error('unexpected success') },
      onError: error => { receivedError = error },
      onFinally: () => { finished = true },
    })
    expect(applied).toBe(false)
    expect(receivedError).toBe(failure)
    expect(finished).toBe(true)
  })

  it('ignores stale responses and stale errors after a newer request starts', async () => {
    const sequence = createRequestSequence()
    let resolveFirst!: (value: string) => void
    let rejectSecond!: (error: Error) => void
    const first = new Promise<string>(resolve => { resolveFirst = resolve })
    const second = new Promise<string>((_resolve, reject) => { rejectSecond = reject })
    const applied: string[] = []
    const errors: unknown[] = []
    const firstRun = runLatestRequest(sequence, () => first, {
      onSuccess: value => applied.push(value), onError: error => errors.push(error),
    })
    const secondRun = runLatestRequest(sequence, () => second, {
      onSuccess: value => applied.push(value), onError: error => errors.push(error),
    })
    resolveFirst('旧范围')
    expect(await firstRun).toBe(false)
    rejectSecond(new Error('旧请求失败'))
    expect(await secondRun).toBe(false)
    expect(applied).toEqual([])
    expect(errors).toEqual([])
  })

  it('keeps the newest successful result when requests finish out of order', async () => {
    const sequence = createRequestSequence()
    let resolveFirst!: (value: string) => void
    let resolveSecond!: (value: string) => void
    const first = new Promise<string>(resolve => { resolveFirst = resolve })
    const second = new Promise<string>(resolve => { resolveSecond = resolve })
    const applied: string[] = []
    const firstRun = runLatestRequest(sequence, () => first, {
      onSuccess: value => applied.push(value), onError: () => undefined,
    })
    const secondRun = runLatestRequest(sequence, () => second, {
      onSuccess: value => applied.push(value), onError: () => undefined,
    })
    resolveSecond('7d')
    expect(await secondRun).toBe(true)
    resolveFirst('24h')
    expect(await firstRun).toBe(false)
    expect(applied).toEqual(['7d'])
  })
})
