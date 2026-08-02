import { describe, expect, it } from 'vitest'
import { REASONING_EFFORT_DISABLE, availableEffortsForMode, nearestEffortToMedium, resolveEffortLevels } from './reasoning-effort'

describe('resolveEffortLevels', () => {
  it('preserves max for Codex and filters client-only efforts', () => {
    expect(resolveEffortLevels({
      reasoning_efforts: ['low', 'xhigh', 'max', 'ultra'],
    }, 'openai-codex')).toEqual(['low', 'xhigh', 'max'])
  })

  it('filters max for generic OpenAI-format clients', () => {
    expect(resolveEffortLevels({
      reasoning_efforts: ['low', 'xhigh', 'max'],
    }, 'openai-responses')).toEqual(['low', 'xhigh'])
  })
})

describe('nearestEffortToMedium', () => {
  it('prefers medium when the model offers it', () => {
    expect(nearestEffortToMedium(['low', 'medium', 'high'])).toBe('medium')
  })

  it('picks the closest tier on either side of medium', () => {
    expect(nearestEffortToMedium(['minimal', 'low'])).toBe('low')
    expect(nearestEffortToMedium(['high', 'max'])).toBe('high')
  })

  it('breaks ties toward the weaker tier', () => {
    expect(nearestEffortToMedium(['low', 'high'])).toBe('low')
  })

  it('resolves by tier distance, not by position in the input', () => {
    expect(nearestEffortToMedium(['max', 'high', 'low', 'none'])).toBe('low')
  })

  it('never returns the disable sentinel that availableEffortsForMode prepends', () => {
    // The old fallback took efforts[0], which is always "disable" — silently
    // turning reasoning off whenever a model lacked the selected tier.
    const selectable = availableEffortsForMode('toggle', ['low', 'high'])
    expect(selectable[0]).toBe(REASONING_EFFORT_DISABLE)
    expect(nearestEffortToMedium(selectable)).toBe('low')
  })

  it('returns empty when no known tier is present', () => {
    expect(nearestEffortToMedium([REASONING_EFFORT_DISABLE, 'turbo'])).toBe('')
    expect(nearestEffortToMedium([])).toBe('')
  })
})
