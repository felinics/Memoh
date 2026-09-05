import { describe, expect, it } from 'vitest'
import type { HandlersContextUsage } from '@memohai/sdk'
import { resolveSessionContextView } from './session-context-view'

const usage: HandlersContextUsage = {
  used_tokens: 10143,
  context_window: 258000,
  breakdown: [
    { kind: 'system_prompt', token_estimate: 1210 },
    { kind: 'conversation_event', token_estimate: 56 },
  ],
  tool_defs: [{ provider: 'native', tools: 39, token_estimate: 8409 }],
  budget_plan: { window: 200000, output_reserve: 32000 },
  compaction: { enabled: true, auto_tokens: 100000 },
}

describe('resolveSessionContextView', () => {
  it('budgets against the persisted plan when the status carries one', () => {
    const view = resolveSessionContextView(usage, { fallbackWindow: null })

    expect(view.estimatedTokens).toBe(9675)
    expect(view.contextWindow).toBe(200000)
    expect(view.outputReserve).toBe(32000)
    expect(view.autoCompactTokens).toBe(100000)
    expect(view.compactionAvailable).toBe(true)
  })

  it('drops plan-derived bands when the status omits the plan (next turn targets another model)', () => {
    const { budget_plan: _omitted, ...withoutPlan } = usage
    const view = resolveSessionContextView(withoutPlan, { fallbackWindow: null })

    expect(view.contextWindow).toBe(258000)
    expect(view.outputReserve).toBeNull()
    expect(view.autoCompactTokens).toBeNull()
  })

  it('hides the mark when auto-compaction is disabled but keeps manual compaction available', () => {
    const view = resolveSessionContextView({ ...usage, compaction: { enabled: false, auto_tokens: 100000 } }, { fallbackWindow: null })

    expect(view.autoCompactTokens).toBeNull()
    expect(view.compactionAvailable).toBe(true)
  })

  it('reports compaction as unavailable when the status carries none (ACP and direct runtimes)', () => {
    const { compaction: _omitted, ...withoutCompaction } = usage
    const view = resolveSessionContextView(withoutCompaction, { fallbackWindow: null })

    expect(view.compactionAvailable).toBe(false)
    expect(view.autoCompactTokens).toBeNull()
  })

  it('falls back to the status window, then the selected model window, and has no estimate without a breakdown', () => {
    expect(resolveSessionContextView({ used_tokens: 5, context_window: 8000 }, { fallbackWindow: 4000 }).contextWindow).toBe(8000)
    const view = resolveSessionContextView({ used_tokens: 5 }, { fallbackWindow: 4000 })
    expect(view.contextWindow).toBe(4000)
    expect(view.estimatedTokens).toBeNull()
    expect(resolveSessionContextView(undefined, { fallbackWindow: null }).contextWindow).toBeNull()
  })
})
