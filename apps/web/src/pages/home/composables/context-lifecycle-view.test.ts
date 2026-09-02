import { describe, expect, it } from 'vitest'
import {
  buildTurnRow,
  classifyPromptDiff,
  compositionFromSnapshot,
  dropReasonRows,
  lifecycleStatusLabelKey,
  lifecycleStatusToneClass,
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
