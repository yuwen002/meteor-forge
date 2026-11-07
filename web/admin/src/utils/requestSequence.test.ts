import { describe, expect, it } from 'vitest'
import { canLoadNextPage, createRequestSequence } from './requestSequence'

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
