import { describe, expect, it } from 'vitest'
import {
  compositionFromSnapshot,
  groupDroppedDecisions,
  lifecycleStatusLabelKey,
  lifecycleStatusToneClass,
} from './context-lifecycle-view'
import type { ContextfragLifecycleSnapshot, ContextfragSelectionDecision } from '@memohai/sdk'

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

  it('excludes selected decisions and keeps trimmed and dropped grouped together', () => {
    const decisions: ContextfragSelectionDecision[] = [
      { decision: 'selected', reason: 'kept', token_estimate: 10 },
      { decision: 'trimmed', reason: 'budget', token_estimate: 20 },
      { decision: 'dropped', reason: 'budget', token_estimate: 30 },
    ]

    expect(groupDroppedDecisions(decisions)).toEqual([
      { reason: 'budget', count: 2, tokens: 50 },
    ])
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

  it('falls back to decision.decision when reason is missing or blank', () => {
    const decisions: ContextfragSelectionDecision[] = [
      { decision: 'dropped', token_estimate: 10 },
      { decision: 'dropped', reason: '   ', token_estimate: 5 },
    ]

    expect(groupDroppedDecisions(decisions)).toEqual([
      { reason: 'dropped', count: 2, tokens: 15 },
    ])
  })

  it('falls back to unknown when neither reason nor decision is present', () => {
    const decisions: ContextfragSelectionDecision[] = [{ token_estimate: 5 }]

    expect(groupDroppedDecisions(decisions)).toEqual([
      { reason: 'unknown', count: 1, tokens: 5 },
    ])
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
