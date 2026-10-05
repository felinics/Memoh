import { describe, expect, it } from 'vitest'
import { isAppStreamEvent } from './useAppStream'

describe('isAppStreamEvent', () => {
  it('accepts a log event for an empty line, whose data the Server omits', () => {
    expect(isAppStreamEvent({ type: 'log', kind: 'dependency', id: 'document-node', stream: 'stderr' })).toBe(true)
    expect(isAppStreamEvent({ type: 'log', kind: 'dependency', id: 'document-node', stream: 'stderr', data: 1 })).toBe(false)
  })
})
