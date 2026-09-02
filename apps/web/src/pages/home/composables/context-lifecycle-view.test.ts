import { describe, expect, it } from 'vitest'
import {
  buildTurnRow,
  classifyPromptDiff,
  compositionFromSnapshot,
  countTrimmed,
  groupDroppedDecisions,
  lifecycleStatusLabelKey,
  lifecycleStatusToneClass,
} from './context-lifecycle-view'
import type { ContextfragLifecycleSnapshot, ContextfragSelectionDecision, HandlersContextLifecycleTurn } from '@memohai/sdk'

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

describe('groupDroppedDecisions', () => {
  it('returns an empty array for null decisions', () => {
    expect(groupDroppedDecisions(null)).toEqual([])
  })

  it('returns an empty array for undefined decisions', () => {
    expect(groupDroppedDecisions(undefined)).toEqual([])
  })

  it('returns an empty array for an empty list', () => {
    expect(groupDroppedDecisions([])).toEqual([])
  })

  it('groups only dropped decisions; trimmed fragments are still in context', () => {
    const decisions: ContextfragSelectionDecision[] = [
      { decision: 'selected', reason: 'kept', token_estimate: 10 },
      { decision: 'trimmed', reason: 'budget', token_estimate: 20 },
      { decision: 'dropped', reason: 'budget', token_estimate: 30 },
    ]

    expect(groupDroppedDecisions(decisions)).toEqual([
      { reason: 'budget', count: 1, tokens: 30 },
    ])
    expect(countTrimmed(decisions)).toBe(1)
    expect(countTrimmed(undefined)).toBe(0)
  })

  it('groups by reason trimmed of surrounding whitespace', () => {
    const decisions: ContextfragSelectionDecision[] = [
      { decision: 'dropped', reason: '  budget exceeded  ', token_estimate: 10 },
      { decision: 'dropped', reason: 'budget exceeded', token_estimate: 15 },
    ]

    expect(groupDroppedDecisions(decisions)).toEqual([
      { reason: 'budget exceeded', count: 2, tokens: 25 },
    ])
  })

  it('falls back to unknown when the reason is missing or blank', () => {
    const decisions: ContextfragSelectionDecision[] = [
      { decision: 'dropped', token_estimate: 10 },
      { decision: 'dropped', reason: '   ', token_estimate: 5 },
    ]

    expect(groupDroppedDecisions(decisions)).toEqual([
      { reason: 'unknown', count: 2, tokens: 15 },
    ])
  })

  it('ignores decisions without a decision kind', () => {
    expect(groupDroppedDecisions([{ token_estimate: 5 }])).toEqual([])
  })

  it('treats a missing token_estimate as zero', () => {
    const decisions: ContextfragSelectionDecision[] = [
      { decision: 'dropped', reason: 'no-size' },
      { decision: 'dropped', reason: 'no-size', token_estimate: 4 },
    ]

    expect(groupDroppedDecisions(decisions)).toEqual([
      { reason: 'no-size', count: 2, tokens: 4 },
    ])
  })

  it('sorts groups by tokens descending', () => {
    const decisions: ContextfragSelectionDecision[] = [
      { decision: 'dropped', reason: 'small', token_estimate: 5 },
      { decision: 'dropped', reason: 'large', token_estimate: 50 },
      { decision: 'dropped', reason: 'medium', token_estimate: 20 },
    ]

    expect(groupDroppedDecisions(decisions).map(g => g.reason)).toEqual(['large', 'medium', 'small'])
  })

  it('breaks a tokens tie by count descending', () => {
    const decisions: ContextfragSelectionDecision[] = [
      { decision: 'dropped', reason: 'single', token_estimate: 20 },
      { decision: 'dropped', reason: 'double', token_estimate: 10 },
      { decision: 'dropped', reason: 'double', token_estimate: 10 },
    ]

    expect(groupDroppedDecisions(decisions).map(g => g.reason)).toEqual(['double', 'single'])
  })

  it('breaks a tokens and count tie by reason ascending', () => {
    const decisions: ContextfragSelectionDecision[] = [
      { decision: 'dropped', reason: 'zeta', token_estimate: 10 },
      { decision: 'dropped', reason: 'alpha', token_estimate: 10 },
    ]

    expect(groupDroppedDecisions(decisions).map(g => g.reason)).toEqual(['alpha', 'zeta'])
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

  it('reports a tool roster change ahead of the prefix change it implies', () => {
    const next = { ...base, stable_prefix_hash: 'h2', tool_defs: [...tools, { provider: 'mcp', name: 'jira', bytes: 300 }] }
    expect(classifyPromptDiff(next, base)).toBe('tools')
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
      selection: { selected: 12, dropped: 3 },
      trust_breakdown: [{ trust: 'system', token_estimate: 1000 }],
    },
  }
  const detail: ContextfragLifecycleSnapshot = {
    selection_decisions: [
      { decision: 'dropped', reason: 'budget', token_estimate: 1500 },
      { decision: 'trimmed', reason: 'stale', token_estimate: 100 },
    ],
  }

  it('summarises selection from the list row and adds trimmed once the detail is known', () => {
    expect(buildTurnRow(turn, { t, formatTime: () => '10:00' }).selection).toBe('chat.lifecycle.selectedCount:12 · chat.lifecycle.droppedCount:3')
    expect(buildTurnRow(turn, { detail, t, formatTime: () => '10:00' }).selection)
      .toBe('chat.lifecycle.selectedCount:12 · chat.lifecycle.droppedCount:3 · chat.lifecycle.trimmedCount:1')
  })

  it('builds the drop-reason section only from the detail decisions', () => {
    const without = buildTurnRow(turn, { t, formatTime: () => '' })
    const withDetail = buildTurnRow(turn, { detail, t, formatTime: () => '' })

    expect(without.sections.map(section => section.key)).toEqual(['trust'])
    expect(withDetail.sections.map(section => section.key)).toEqual(['dropReasons', 'trust'])
    expect(withDetail.sections[0]?.rows).toEqual([{ key: 'budget', label: 'budget', value: '1 · 1.5K' }])
  })

  it('carries the prompt-diff label key, or none at an unknown boundary', () => {
    expect(buildTurnRow(turn, { previous: null, t, formatTime: () => '' }).diffKey).toBe('chat.lifecycle.diffInitial')
    expect(buildTurnRow(turn, { t, formatTime: () => '' }).diffKey).toBeNull()
  })
})
