import { describe, expect, it } from 'vitest'
import {
  buildTurnRow,
  classifyPromptDiff,
  compositionFromSnapshot,
  dropReasonRows,
  lifecycleStatusLabelKey,
  lifecycleStatusToneClass,
  compactLifecyclePages,
  lifecycleGapBefore,
  lifecycleGapJoins,
  mergeLifecyclePages,
} from './context-lifecycle-view'
import type { ContextfragLifecycleSnapshot, ContextfragSelectionTrace, HandlersContextLifecycleTurn } from '@memohai/sdk'

describe('compositionFromSnapshot', () => {
  it('computes a composition from a snapshot breakdown and tool_defs', () => {
    const snapshot: ContextfragLifecycleSnapshot = {
      breakdown: [
        { kind: 'system_prompt', token_estimate: 100 },
        { kind: 'memory_recall', token_estimate: 50 },
      ],
      tool_defs: [
        { provider: 'anthropic', name: 'web_search', token_estimate: 25 },
      ],
    }

    expect(compositionFromSnapshot(snapshot)).toEqual({
      categories: [
        { id: 'system', tokens: 100, colorClass: 'bg-accent-gray' },
        { id: 'tools', tokens: 25, colorClass: 'bg-accent-purple' },
        { id: 'memory', tokens: 50, colorClass: 'bg-accent-teal' },
      ],
      totalTokens: 175,
    })
  })

  it('returns null for a null snapshot', () => {
    expect(compositionFromSnapshot(null)).toBeNull()
  })

  it('returns null for an undefined snapshot', () => {
    expect(compositionFromSnapshot(undefined)).toBeNull()
  })

  it('returns null when breakdown and tool_defs are both absent', () => {
    expect(compositionFromSnapshot({})).toBeNull()
  })

  it('returns null (display gate) when entries exist but total zero tokens', () => {
    expect(compositionFromSnapshot({ breakdown: [{ kind: 'system_prompt', token_estimate: 0 }] })).toBeNull()
  })
})

describe('dropReasonRows', () => {
  it('returns nothing without a selection trace or drop reasons', () => {
    expect(dropReasonRows(undefined)).toEqual([])
    expect(dropReasonRows({ selected: 3, dropped: 0 })).toEqual([])
  })

  it('pairs each reason count with its rolled-up token cost, sorted by tokens then count then name', () => {
    const selection: ContextfragSelectionTrace = {
      selected: 10,
      dropped: 6,
      drop_reasons: { history_budget: 3, retention_tier_evicted: 2, unknown: 1 },
      drop_reason_tokens: { history_budget: 500, retention_tier_evicted: 900, unknown: 5 },
    }

    expect(dropReasonRows(selection)).toEqual([
      { reason: 'retention_tier_evicted', count: 2, tokens: 900 },
      { reason: 'history_budget', count: 3, tokens: 500 },
      { reason: 'unknown', count: 1, tokens: 5 },
    ])
  })

  it('keeps count-only rows for snapshots persisted before the token rollup existed', () => {
    expect(dropReasonRows({ selected: 1, dropped: 2, drop_reasons: { history_budget: 2 } })).toEqual([
      { reason: 'history_budget', count: 2, tokens: null },
    ])
  })
})

describe('lifecycleStatusToneClass', () => {
  it.each([
    ['completed', 'text-muted-foreground'],
    ['fallback', 'text-warning'],
    ['failed_budget', 'text-destructive'],
    ['failed_provider', 'text-destructive'],
    ['aborted', 'text-destructive'],
    ['unknown_status', 'text-muted-foreground'],
    [null, 'text-muted-foreground'],
    [undefined, 'text-muted-foreground'],
  ])('maps %s to %s', (status, expected) => {
    expect(lifecycleStatusToneClass(status)).toBe(expected)
  })
})

describe('lifecycleStatusLabelKey', () => {
  it.each([
    ['completed', 'chat.lifecycle.statusCompleted'],
    ['fallback', 'chat.lifecycle.statusFallback'],
    ['failed_budget', 'chat.lifecycle.statusFailedBudget'],
    ['failed_provider', 'chat.lifecycle.statusFailedProvider'],
    ['aborted', 'chat.lifecycle.statusAborted'],
    ['unknown_status', null],
    [null, null],
    [undefined, null],
  ])('maps %s to %s', (status, expected) => {
    expect(lifecycleStatusLabelKey(status)).toBe(expected)
  })
})

describe('classifyPromptDiff', () => {
  const tools = [{ provider: 'native', name: 'read', bytes: 900 }]
  const base: ContextfragLifecycleSnapshot = { stable_prefix_hash: 'h1', tool_defs: tools }

  it('labels the first turn of a session', () => {
    expect(classifyPromptDiff(base, null)).toBe('initial')
  })

  it('stays silent when the older turn is unknown (page boundary)', () => {
    expect(classifyPromptDiff(base, undefined)).toBeNull()
  })

  it('reports a tool roster change alone when the stable prefix held', () => {
    const next = { ...base, tool_defs: [...tools, { provider: 'mcp', name: 'jira', bytes: 300 }] }
    expect(classifyPromptDiff(next, base)).toBe('tools')
  })

  it('reports system and tools together when both moved', () => {
    const next = { ...base, stable_prefix_hash: 'h2', tool_defs: [...tools, { provider: 'mcp', name: 'jira', bytes: 300 }] }
    expect(classifyPromptDiff(next, base)).toBe('system_tools')
  })

  it('reports a system change when only the stable prefix hash moved', () => {
    expect(classifyPromptDiff({ ...base, stable_prefix_hash: 'h2' }, base)).toBe('system')
  })

  it('reports history-only when prefix and tools are unchanged', () => {
    expect(classifyPromptDiff({ ...base }, base)).toBe('history')
  })

  it('stays silent without prefix hashes unless the tools changed', () => {
    expect(classifyPromptDiff({ tool_defs: tools }, { tool_defs: tools })).toBeNull()
    expect(classifyPromptDiff({ tool_defs: [] }, { tool_defs: tools })).toBe('tools')
  })
})

describe('buildTurnRow', () => {
  const t = (key: string, params?: Record<string, unknown>) => (params ? `${key}:${Object.values(params).join(',')}` : key)
  const turn: HandlersContextLifecycleTurn = {
    run_id: 'run-a',
    created_at: '2026-08-31T10:00:00.000Z',
    status: 'completed',
    snapshot: {
      model: 'm',
      breakdown: [{ kind: 'system_prompt', token_estimate: 1000 }],
      selection: { selected: 12, dropped: 3, trimmed: 1, drop_reasons: { budget: 3 }, drop_reason_tokens: { budget: 1500 } },
      trust_breakdown: [{ trust: 'system', token_estimate: 1000 }],
    },
  }

  it('summarises selection counts, including trimmed fragments, from the bounded trace', () => {
    expect(buildTurnRow(turn, { t, formatTime: () => '10:00' }).selection)
      .toBe('chat.lifecycle.selectedCount:12 · chat.lifecycle.droppedCount:3 · chat.lifecycle.trimmedCount:1')
  })

  it('builds the drop-reason section from the rolled-up trace, never from per-fragment decisions', () => {
    const row = buildTurnRow(turn, { t, formatTime: () => '' })

    expect(row.sections.map(section => section.key)).toEqual(['dropReasons', 'trust'])
    expect(row.sections[0]?.rows).toEqual([{ key: 'budget', label: 'budget', value: '3 · 1.5K' }])
  })

  it('shows a count-only drop reason when the snapshot predates the token rollup', () => {
    const legacy: HandlersContextLifecycleTurn = { ...turn, snapshot: { ...turn.snapshot, selection: { selected: 1, dropped: 2, drop_reasons: { budget: 2 } } } }

    expect(buildTurnRow(legacy, { t, formatTime: () => '' }).sections[0]?.rows).toEqual([{ key: 'budget', label: 'budget', value: '2' }])
  })

  it('carries the prompt-diff label key, or none at an unknown boundary', () => {
    expect(buildTurnRow(turn, { previous: null, t, formatTime: () => '' }).diffKey).toBe('chat.lifecycle.diffInitial')
    expect(buildTurnRow(turn, { t, formatTime: () => '' }).diffKey).toBeNull()
  })
})

describe('mergeLifecyclePages fragment previews', () => {
  it('unions the previews of every joined page', () => {
    const first = { turns: [{ run_id: 'run-2' }], has_more: true, next_cursor: 'c1', fragment_previews: { h1: { preview: 'one' } } }
    const older = { turns: [{ run_id: 'run-1' }], has_more: false, fragment_previews: { h2: { preview: 'two' } } }
    const merged = mergeLifecyclePages(first, [older])
    expect(Object.keys(merged.fragmentPreviews).sort()).toEqual(['h1', 'h2'])
    expect(mergeLifecyclePages(null, []).fragmentPreviews).toEqual({})
  })
})

describe('mergeLifecyclePages', () => {
  it('concatenates newest-first pages and drops runs repeated across page boundaries', () => {
    const turn = (runId: string): HandlersContextLifecycleTurn => ({ run_id: runId, created_at: '2026-09-03T00:00:00.000Z', snapshot: {} })
    const merged = mergeLifecyclePages(
      { turns: [turn('r9'), turn('r8')], has_more: true, next_cursor: 'c1', limit: 2 },
      [
        { turns: [turn('r8'), turn('r7')], has_more: true, next_cursor: 'c2', limit: 2 },
        { turns: [turn('r6')], has_more: false, limit: 2 },
      ],
    )
    expect(merged.turns.map(item => item.run_id)).toEqual(['r9', 'r8', 'r7', 'r6'])
    expect(merged.hasMore).toBe(false)
    expect(merged.nextCursor).toBeNull()
    expect(mergeLifecyclePages(null, []).turns).toEqual([])
    // Older pages are immutable keyset slices: a first page that moved on
    // keeps them, and only the runs between the two are missing.
    expect(mergeLifecyclePages(
      { turns: [turn('r10'), turn('r9')], has_more: true, next_cursor: 'c-new', limit: 2 },
      [{ turns: [turn('r8')], has_more: false, limit: 2 }],
    ).turns.map(item => item.run_id)).toEqual(['r10', 'r9', 'r8'])
    expect(mergeLifecyclePages({ turns: [turn('r1')], has_more: true, next_cursor: 'c', limit: 1 }, []).nextCursor).toBe('c')
    expect(mergeLifecyclePages({ turns: [turn('r1')], has_more: true, limit: 1 }, []).nextCursor).toBeNull()
  })
})

describe('compactLifecyclePages', () => {
  it('folds pages into one immutable slice keyed newest first', () => {
    const turn = (runId: string): HandlersContextLifecycleTurn => ({ run_id: runId, created_at: '2026-09-03T00:00:00.000Z', snapshot: {} })
    const compact = compactLifecyclePages([
      { turns: [turn('r8'), turn('r7')], has_more: true, next_cursor: 'c-gap', limit: 8, fragment_previews: { h8: { preview: 'eight' } } },
      { turns: [turn('r7'), turn('r6')], has_more: true, next_cursor: 'c2', limit: 50, fragment_previews: { h6: { preview: 'six' } } },
      { turns: [turn('r5')], has_more: false, limit: 50 },
    ])
    expect(compact.turns?.map(item => item.run_id)).toEqual(['r8', 'r7', 'r6', 'r5'])
    expect(compact.has_more).toBe(false)
    expect(compact.next_cursor).toBeUndefined()
    expect(Object.keys(compact.fragment_previews ?? {}).sort()).toEqual(['h6', 'h8'])
    expect(compactLifecyclePages([]).turns).toEqual([])
  })
})

describe('lifecycleGapBefore', () => {
  it('names the cursor to fill only when loaded older pages no longer join the first page', () => {
    expect(lifecycleGapBefore('c-new', 'c-old', true)).toBe('c-new')
    expect(lifecycleGapBefore('c-old', 'c-old', true)).toBeNull()
    expect(lifecycleGapBefore('c-new', 'c-old', false)).toBeNull()
    expect(lifecycleGapBefore(undefined, 'c-old', true)).toBeNull()
    expect(lifecycleGapBefore('c-new', null, true)).toBeNull()
  })
})

describe('lifecycleGapJoins', () => {
  const seq = (hi: number, lo: number): HandlersContextLifecycleTurn[] => Array.from({ length: hi - lo + 1 }, (_, i) => ({ run_id: `r${hi - i}`, created_at: '', snapshot: {} }))

  it('continues past a gap page that reaches neither the window nor the end, and stops once it does', () => {
    const older = [{ turns: seq(50, 1), has_more: false, limit: 50, aggregate_scope: '', aggregates: { turns: 50, total_cache_read_tokens: 0, total_cache_write_tokens: 0 } }]
    const first = { turns: seq(62, 55), has_more: true, next_cursor: 'c55', limit: 8, aggregate_scope: '', aggregates: { turns: 8, total_cache_read_tokens: 0, total_cache_write_tokens: 0 } }
    const second = { turns: seq(54, 47), has_more: true, next_cursor: 'c47', limit: 8, aggregate_scope: '', aggregates: { turns: 8, total_cache_read_tokens: 0, total_cache_write_tokens: 0 } }
    expect(lifecycleGapJoins(first, older)).toBe(false)
    expect(lifecycleGapJoins(second, older)).toBe(true)
    expect(lifecycleGapJoins({ ...first, has_more: false }, older)).toBe(true)
    // Twelve runs finished between refetches: two gap pages bridge them all.
    const fresh = { turns: seq(112, 63), has_more: true, next_cursor: 'c63', limit: 50, aggregate_scope: '', aggregates: { turns: 50, total_cache_read_tokens: 0, total_cache_write_tokens: 0 } }
    const merged = mergeLifecyclePages(fresh, [compactLifecyclePages([first, second, ...older])])
    expect(merged.turns.length).toBe(112)
    expect(merged.hasMore).toBe(false)
  })
})
