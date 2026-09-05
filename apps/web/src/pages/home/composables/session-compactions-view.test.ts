import { describe, expect, it } from 'vitest'
import type { HandlersSessionCompactionsResponse } from '@memohai/sdk'
import { mergeCompactionPages } from './session-compactions-view'

const at = (hour: number) => `2026-09-05T${String(hour).padStart(2, '0')}:00:00.000Z`
const page = (ids: number[], extra: Partial<HandlersSessionCompactionsResponse> = {}): HandlersSessionCompactionsResponse => ({
  items: ids.map(id => ({ id: `c${id}`, status: 'ok', started_at: at(id) })),
  has_more: false,
  ...extra,
})

describe('mergeCompactionPages', () => {
  it('joins newest-first pages into one oldest-first list and keeps the oldest page\'s cursor', () => {
    const merged = mergeCompactionPages(
      page([9, 8], { has_more: true, next_cursor: 'c8' }),
      [page([8, 7], { has_more: true, next_cursor: 'c7' }), page([6])],
    )
    expect(merged.items.map(item => item.id)).toEqual(['c6', 'c7', 'c8', 'c9'])
    expect(merged.hasMore).toBe(false)
    expect(merged.nextCursor).toBeNull()
    expect(mergeCompactionPages(page([3], { has_more: true, next_cursor: 'c3' }), []).nextCursor).toBe('c3')
    expect(mergeCompactionPages(null, []).items).toEqual([])
  })

  it('skips compactions without a start time, which cannot be placed', () => {
    const merged = mergeCompactionPages({ items: [{ id: 'x', status: 'ok' }, { id: 'y', status: 'ok', started_at: at(1) }], has_more: false }, [])
    expect(merged.items.map(item => item.id)).toEqual(['y'])
  })
})
