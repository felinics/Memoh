import { describe, expect, it } from 'vitest'
import { REASONING_EFFORT_DISABLE, availableEffortsForMode, nearestEffortToMedium, resolveEffortLevels } from './reasoning-effort'

describe('resolveEffortLevels', () => {
  it('keeps disable, which declares that off is achievable', () => {
    expect(resolveEffortLevels({
      reasoning_efforts: [REASONING_EFFORT_DISABLE, 'low', 'medium', 'high'],
    }, 'openai-completions')).toEqual([REASONING_EFFORT_DISABLE, 'low', 'medium', 'high'])
  })

  it('rewrites the legacy off spelling instead of dropping it', () => {
    // A config still advertising "none" describes a model that can be turned off.
    // Dropping it would hide Off from a model that supports it.
    expect(resolveEffortLevels({
      reasoning_efforts: ['none', 'low', 'medium', 'high'],
    }, 'openai-completions')).toEqual([REASONING_EFFORT_DISABLE, 'low', 'medium', 'high'])
  })

  it('collapses both spellings of off into one entry', () => {
    expect(resolveEffortLevels({
      reasoning_efforts: ['none', REASONING_EFFORT_DISABLE, 'low', 'high'],
    }, 'openai-completions')).toEqual([REASONING_EFFORT_DISABLE, 'low', 'high'])
  })

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

describe('availableEffortsForMode', () => {
  it('offers off once, at the front, when the model advertises it', () => {
    const selectable = availableEffortsForMode('adaptive', resolveEffortLevels({
      reasoning_efforts: ['low', REASONING_EFFORT_DISABLE, 'medium', 'high'],
    }, 'openai-completions'))
    expect(selectable).toEqual([REASONING_EFFORT_DISABLE, 'low', 'medium', 'high'])
  })

  it('offers no off for a model that cannot be turned off', () => {
    // A model that never declared it can be turned off has no off shape to send,
    // so showing Off there is a control that does nothing.
    const selectable = availableEffortsForMode('adaptive', resolveEffortLevels({
      reasoning_efforts: ['low', 'medium', 'high'],
    }, 'openai-completions'))
    expect(selectable).toEqual(['low', 'medium', 'high'])
  })

  it('offers off on the Anthropic wire, where off needs no declared token', () => {
    // Claude never advertises the token, but a toggle model is off whenever the
    // thinking field is absent — which is what the adaptor sends when disabled.
    const selectable = availableEffortsForMode('toggle', resolveEffortLevels({
      thinking_mode: 'toggle',
      reasoning_efforts: ['low', 'medium', 'high'],
    }, 'anthropic-messages'), 'anthropic-messages')
    expect(selectable).toEqual([REASONING_EFFORT_DISABLE, 'low', 'medium', 'high'])
  })

  it('offers no off for adaptive Anthropic models, where omission is not off', () => {
    // Adaptive models think on their own, so omitting the field leaves that
    // default in charge instead of turning thinking off.
    const selectable = availableEffortsForMode('adaptive', resolveEffortLevels({
      thinking_mode: 'adaptive',
      reasoning_efforts: ['low', 'medium', 'high', 'max'],
    }, 'anthropic-messages'), 'anthropic-messages')
    expect(selectable).toEqual(['low', 'medium', 'high', 'max'])
  })

  it('offers nothing for a model with no thinking concept', () => {
    expect(availableEffortsForMode('none', ['low', 'medium'])).toEqual([])
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

  it('never returns off, so an active config cannot resolve to disabled', () => {
    // The old fallback took efforts[0], which was always "disable" — silently
    // turning reasoning off whenever a model lacked the selected tier.
    const selectable = availableEffortsForMode('toggle', [REASONING_EFFORT_DISABLE, 'low', 'high'])
    expect(selectable[0]).toBe(REASONING_EFFORT_DISABLE)
    expect(nearestEffortToMedium(selectable)).toBe('low')
  })

  it('returns empty when no known tier is present', () => {
    expect(nearestEffortToMedium([REASONING_EFFORT_DISABLE, 'turbo'])).toBe('')
    expect(nearestEffortToMedium([])).toBe('')
  })
})
