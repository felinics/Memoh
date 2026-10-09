import { describe, expect, it } from 'vitest'
import { compactionNoticeKey } from './compaction-notice'

describe('compactionNoticeKey', () => {
  it('reports success only for a committed summary', () => {
    expect(compactionNoticeKey({ status: 'ok', message_count: 4 })).toBeUndefined()
  })

  it('tells held-back history apart from nothing to compact', () => {
    expect(compactionNoticeKey({ status: 'noop', reason: 'no_beneficial_span' })).toBe('chat.compactBlocked')
    expect(compactionNoticeKey({ status: 'noop', reason: 'read_budget_exceeded' })).toBe('chat.compactBlocked')
    expect(compactionNoticeKey({ status: 'noop', reason: 'nothing_to_compact' })).toBe('chat.compactNothing')
    expect(compactionNoticeKey({ status: 'noop' })).toBe('chat.compactNothing')
  })
})
