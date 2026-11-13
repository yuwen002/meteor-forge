import { describe, expect, it } from 'vitest'
import { createRequestSequence, runLatestRequest } from './requestSequence'

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
