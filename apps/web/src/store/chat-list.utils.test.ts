import { describe, expect, it } from 'vitest'
import { computeBgTaskPill, latestOutputLine, normalizedRuntimeType, normalizedSessionMode, provisionalSessionTitle, sortByRecency, upsertById } from './chat-list.utils'

describe('chat-list.utils', () => {
  it('replaces existing item with same id and preserves order', () => {
    const items = [
      { id: 2, content: 'second' },
      { id: 4, content: 'fourth' },
    ]

    expect(upsertById(items, { id: 2, content: 'updated' })).toEqual([
      { id: 2, content: 'updated' },
      { id: 4, content: 'fourth' },
    ])
  })

  it('inserts new item and sorts by id', () => {
    const items = [
      { id: 4, content: 'fourth' },
      { id: 8, content: 'eighth' },
    ]

    expect(upsertById(items, { id: 6, content: 'sixth' })).toEqual([
      { id: 4, content: 'fourth' },
      { id: 6, content: 'sixth' },
      { id: 8, content: 'eighth' },
    ])
  })

  it('updates an existing item in place, preserving array and item identity', () => {
    const original = { id: 2, content: 'second' }
    const items = [original, { id: 4, content: 'fourth' }]

    const result = upsertById(items, { id: 2, content: 'updated' })

    expect(result).toBe(items)
    expect(result[0]).toBe(original)
    expect(original.content).toBe('updated')
  })

  it('drops fields absent from the incoming snapshot when updating in place', () => {
    const original: { id: number; content: string; stale?: boolean } = {
      id: 2,
      content: 'second',
      stale: true,
    }
    const items = [original]

    upsertById(items, { id: 2, content: 'updated' })

    expect(original).toEqual({ id: 2, content: 'updated' })
  })

  it('sortByRecency orders by updated_at desc, falls back to created_at, stable on ties', () => {
    const a = { id: 'a', updated_at: '2026-01-01T00:00:00Z' }
    const b = { id: 'b', updated_at: '2026-01-03T00:00:00Z' }
    const c = { id: 'c', created_at: '2026-01-02T00:00:00Z' }
    const d = { id: 'd' }
    const e = { id: 'e', updated_at: '2026-01-03T00:00:00Z' }

    expect(sortByRecency<{
      id: string
      updated_at?: string
      created_at?: string
    }>([a, b, c, d, e]).map(x => x.id)).toEqual(['b', 'e', 'c', 'a', 'd'])
  })

  it('sortByRecency does not mutate its input', () => {
    const input = [
      { id: 'a', updated_at: '2026-01-01T00:00:00Z' },
      { id: 'b', updated_at: '2026-01-03T00:00:00Z' },
    ]

    sortByRecency(input)

    expect(input.map(x => x.id)).toEqual(['a', 'b'])
  })

  it('normalizes legacy ACP session descriptors', () => {
    const legacyACPChat = { type: 'acp_agent' }
    const schedule = { type: 'schedule' }

    expect(normalizedSessionMode(legacyACPChat)).toBe('chat')
    expect(normalizedRuntimeType(legacyACPChat)).toBe('acp_agent')
    expect(normalizedSessionMode(schedule)).toBe('schedule')
    expect(normalizedRuntimeType(schedule)).toBe('model')
  })

  it('latestOutputLine returns the last non-empty line, trimmed', () => {
    expect(latestOutputLine('alpha\nbeta\ngamma')).toBe('gamma')
    expect(latestOutputLine('alpha\nbeta\n')).toBe('beta')
    expect(latestOutputLine('alpha\n\n')).toBe('alpha')
    expect(latestOutputLine('  hi  ')).toBe('hi')
  })

  it('latestOutputLine collapses carriage-return progress to the current segment', () => {
    expect(latestOutputLine('downloading 10%\rdownloading 80%')).toBe('downloading 80%')
    expect(latestOutputLine('build\nstep 1\r')).toBe('step 1')
  })

  it('latestOutputLine returns empty for empty, whitespace, or missing input', () => {
    expect(latestOutputLine('')).toBe('')
    expect(latestOutputLine(undefined)).toBe('')
    expect(latestOutputLine('\r\n')).toBe('')
    expect(latestOutputLine('   \n  ')).toBe('')
  })
})

describe('computeBgTaskPill', () => {
  const beacon = (taskId: string, phase: 'active' | 'done', visible: boolean, latestLine = '') =>
    ({ taskId, phase, visible, latestLine })

  it('shows no pill when there are no beacons', () => {
    expect(computeBgTaskPill([])).toBeNull()
  })

  it('shows no pill when the only running task is on screen', () => {
    expect(computeBgTaskPill([beacon('t1', 'active', true, 'step 1')])).toBeNull()
  })

  it('shows a running pill for an off-screen running task', () => {
    expect(computeBgTaskPill([beacon('t1', 'active', false, 'step 7')])).toEqual({
      tone: 'running',
      count: 1,
      latestLine: 'step 7',
    })
  })

  it('counts only off-screen running tasks and uses the latest one for the line', () => {
    expect(computeBgTaskPill([
      beacon('t1', 'active', false, 'first'),
      beacon('t2', 'active', true, 'on screen'),
      beacon('t3', 'active', false, 'latest'),
    ])).toEqual({ tone: 'running', count: 2, latestLine: 'latest' })
  })

  it('shows a done pill when an off-screen task has completed and none are running', () => {
    expect(computeBgTaskPill([beacon('t1', 'done', false)])).toEqual({
      tone: 'done',
      count: 1,
      latestLine: '',
    })
  })

  it('prefers the running tone over done when both are off-screen', () => {
    expect(computeBgTaskPill([
      beacon('t1', 'done', false),
      beacon('t2', 'active', false, 'working'),
    ])).toEqual({ tone: 'running', count: 1, latestLine: 'working' })
  })
})

describe('provisionalSessionTitle', () => {
  it('returns the trimmed first line as-is when short', () => {
    expect(provisionalSessionTitle('How do I parse JSON in Go?')).toBe('How do I parse JSON in Go?')
  })

  it('uses only the first line, dropping the rest', () => {
    expect(provisionalSessionTitle('Summarize this file\n\nHere is the content:\nlots\nof\nlines')).toBe('Summarize this file')
  })

  it('truncates to the cap and appends an ellipsis', () => {
    expect(provisionalSessionTitle('A'.repeat(120))).toBe('A'.repeat(50) + '…')
  })

  it('counts runes, not utf-16 units, when truncating CJK text', () => {
    expect(provisionalSessionTitle('あ'.repeat(80))).toBe('あ'.repeat(50) + '…')
  })

  it('strips markdown so the label stays a clean one-line peek', () => {
    expect(provisionalSessionTitle('## Title\n`code` and [a link](https://x)')).toBe('Title')
  })

  it('strips emphasis markers so it matches the backend fallback', () => {
    expect(provisionalSessionTitle('**bold** and *italic* here')).toBe('bold and italic here')
  })

  it('returns empty when the first line has no usable text', () => {
    expect(provisionalSessionTitle('```js\nconst x = 1\n```')).toBe('')
  })
})
