import { describe, expect, it } from 'vitest'
import { computeContextComposition, contextPressureToneClass, formatTokenCount } from './context-categories'
import type { ContextfragKind, ContextfragKindBreakdown, HandlersContextUsage, HandlersToolDefBucket } from '@memohai/sdk'

function frag(kind: ContextfragKind, tokenEstimate?: number): ContextfragKindBreakdown {
  return tokenEstimate === undefined ? { kind } : { kind, token_estimate: tokenEstimate }
}

function toolDef(provider: string, tokenEstimate: number): HandlersToolDefBucket {
  return { provider, token_estimate: tokenEstimate }
}

function usage(overrides: Partial<HandlersContextUsage>): HandlersContextUsage {
  return { breakdown: [], tool_defs: [], ...overrides }
}

describe('computeContextComposition', () => {
  it('maps each of the 17 known kinds to its category and folds an unknown kind into other', () => {
    const input = usage({
      breakdown: [
        frag('system_prompt', 10),
        frag('system_policy', 20),
        frag('bot_identity', 30),
        frag('platform_identity', 50),
        frag('workspace_instruction', 40),
        frag('tool_usage', 60),
        frag('conversation_event', 70),
        frag('current_user_message', 80),
        frag('attachment_ref', 90),
        frag('native_image', 100),
        frag('runtime_context', 150),
        frag('skills_catalog', 110),
        frag('memory_recall', 160),
        frag('conversation_summary', 170),
        frag('hook_context', 120),
        frag('injected_message', 130),
        frag('background_summary', 140),
        frag('future_kind_not_yet_known' as ContextfragKind, 5),
      ],
    })

    expect(computeContextComposition(input)).toEqual({
      categories: [
        { id: 'system', tokens: 110, colorClass: 'bg-accent-gray' },
        { id: 'rules', tokens: 40, colorClass: 'bg-accent-green' },
        { id: 'tools', tokens: 60, colorClass: 'bg-accent-purple' },
        { id: 'skills', tokens: 110, colorClass: 'bg-accent-yellow' },
        { id: 'memory', tokens: 160, colorClass: 'bg-accent-teal' },
        { id: 'summary', tokens: 170, colorClass: 'bg-accent-brown' },
        { id: 'conversation', tokens: 490, colorClass: 'bg-accent-orange' },
        { id: 'other', tokens: 395, colorClass: 'bg-accent-blue' },
      ],
      totalTokens: 1535,
    })
  })

  it('folds an empty-string kind into other and counts it in totalTokens', () => {
    const input = usage({ breakdown: [frag('' as ContextfragKind, 7), frag('system_prompt', 10)] })

    expect(computeContextComposition(input)).toEqual({
      categories: [
        { id: 'system', tokens: 10, colorClass: 'bg-accent-gray' },
        { id: 'other', tokens: 7, colorClass: 'bg-accent-blue' },
      ],
      totalTokens: 17,
    })
  })

  it('sums tool_defs token_estimate into tools alongside the tool_usage kind', () => {
    const input = usage({
      breakdown: [frag('tool_usage', 40)],
      tool_defs: [toolDef('anthropic', 15), toolDef('openai', 25)],
    })

    expect(computeContextComposition(input)).toEqual({
      categories: [{ id: 'tools', tokens: 80, colorClass: 'bg-accent-purple' }],
      totalTokens: 80,
    })
  })

  it('produces a tools-only composition from tool_defs alone when breakdown is empty', () => {
    const input = usage({ tool_defs: [toolDef('anthropic', 30)] })

    expect(computeContextComposition(input)).toEqual({
      categories: [{ id: 'tools', tokens: 30, colorClass: 'bg-accent-purple' }],
      totalTokens: 30,
    })
  })

  it('orders categories by the fixed CONTEXT_CATEGORY_IDS order regardless of input order', () => {
    const input = usage({
      breakdown: [
        frag('conversation_summary', 10),
        frag('memory_recall', 10),
        frag('hook_context', 10),
        frag('skills_catalog', 10),
        frag('workspace_instruction', 10),
        frag('conversation_event', 10),
        frag('system_prompt', 10),
        frag('tool_usage', 10),
      ],
    })

    expect(computeContextComposition(input)?.categories.map(category => category.id)).toEqual([
      'system',
      'rules',
      'tools',
      'skills',
      'memory',
      'summary',
      'conversation',
      'other',
    ])
  })

  it('omits categories with zero or missing token totals', () => {
    const input = usage({
      breakdown: [
        frag('system_prompt', 0),
        frag('workspace_instruction'),
        frag('memory_recall', 25),
      ],
    })

    expect(computeContextComposition(input)?.categories).toEqual([
      { id: 'memory', tokens: 25, colorClass: 'bg-accent-teal' },
    ])
  })

  it('returns null when entries exist but carry zero tokens, so callers never render an empty bar', () => {
    const input = usage({ breakdown: [frag('system_prompt', 0)], tool_defs: [toolDef('anthropic', 0)] })

    expect(computeContextComposition(input)).toBeNull()
  })

  it('returns null when breakdown and tool_defs are both empty', () => {
    expect(computeContextComposition(usage({}))).toBeNull()
  })

  it('returns null when breakdown and tool_defs are both absent', () => {
    expect(computeContextComposition({})).toBeNull()
  })

  it('returns null for falsy usage', () => {
    expect(computeContextComposition(null)).toBeNull()
    expect(computeContextComposition(undefined)).toBeNull()
  })

  it('sums totalTokens across every emitted category including tool defs', () => {
    const input = usage({
      breakdown: [frag('system_prompt', 100), frag('memory_recall', 50)],
      tool_defs: [toolDef('anthropic', 25)],
    })

    expect(computeContextComposition(input)?.totalTokens).toBe(175)
  })
})

describe('contextPressureToneClass', () => {
  it('stays neutral below the warning threshold', () => {
    expect(contextPressureToneClass(69.9, 'text')).toBe('text-foreground')
    expect(contextPressureToneClass(69.9, 'bg')).toBe('bg-foreground')
  })

  it('warns from 70 inclusive', () => {
    expect(contextPressureToneClass(70, 'text')).toBe('text-warning')
    expect(contextPressureToneClass(70, 'bg')).toBe('bg-warning')
  })

  it('stays warning below the destructive threshold', () => {
    expect(contextPressureToneClass(89.9, 'text')).toBe('text-warning')
    expect(contextPressureToneClass(89.9, 'bg')).toBe('bg-warning')
  })

  it('turns destructive from 90 inclusive', () => {
    expect(contextPressureToneClass(90, 'text')).toBe('text-destructive')
    expect(contextPressureToneClass(90, 'bg')).toBe('bg-destructive')
  })
})

describe('formatTokenCount', () => {
  it('returns the plain number below 1000', () => {
    expect(formatTokenCount(999)).toBe('999')
  })

  it('formats thousands with one decimal and a K suffix', () => {
    expect(formatTokenCount(1500)).toBe('1.5K')
  })

  it('formats millions with one decimal and an M suffix', () => {
    expect(formatTokenCount(2_400_000)).toBe('2.4M')
  })

  it('formats zero as the plain number', () => {
    expect(formatTokenCount(0)).toBe('0')
  })
})
