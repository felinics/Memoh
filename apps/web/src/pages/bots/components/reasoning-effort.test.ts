import { describe, expect, it } from 'vitest'
import { REASONING_EFFORT_DISABLE, reconcileStoredEffort, selectableEfforts } from './reasoning-effort'

// The capability questions these used to answer — which tiers a model offers,
// whether off is reachable, how max normalizes — now live in internal/reasoning
// and arrive resolved on the model's `reasoning` field. What is left to test is
// rendering the answer and migrating a stored value, which is all this file does.

describe('selectableEfforts', () => {
  it('offers off at the front when the server says off is reachable', () => {
    expect(selectableEfforts({
      supported: true,
      can_disable: true,
      efforts: ['low', 'medium', 'high'],
      default_effort: 'medium',
    })).toEqual([REASONING_EFFORT_DISABLE, 'low', 'medium', 'high'])
  })

  it('offers no off for a model that cannot be turned off', () => {
    expect(selectableEfforts({
      supported: true,
      can_disable: false,
      efforts: ['low', 'medium', 'high'],
      default_effort: 'medium',
    })).toEqual(['low', 'medium', 'high'])
  })

  it('offers nothing for a model with no thinking concept', () => {
    expect(selectableEfforts({ supported: false })).toEqual([])
  })

  it('offers nothing when the field is absent, as it is for non-chat models', () => {
    expect(selectableEfforts(undefined)).toEqual([])
    expect(selectableEfforts(null)).toEqual([])
  })
})

describe('reconcileStoredEffort', () => {
  const options = {
    supported: true,
    can_disable: true,
    efforts: ['low', 'medium', 'high'],
    default_effort: 'medium',
  }

  it('keeps a tier the model still offers', () => {
    expect(reconcileStoredEffort('high', options)).toBe('high')
  })

  it('lands a stranded tier on the model default', () => {
    expect(reconcileStoredEffort('xhigh', options)).toBe('medium')
  })

  it('keeps off when off is still reachable', () => {
    expect(reconcileStoredEffort(REASONING_EFFORT_DISABLE, options)).toBe(REASONING_EFFORT_DISABLE)
  })

  it('treats the legacy off spelling as off', () => {
    expect(reconcileStoredEffort('none', options)).toBe(REASONING_EFFORT_DISABLE)
  })

  it('moves off to the default when the new model cannot be turned off', () => {
    expect(reconcileStoredEffort(REASONING_EFFORT_DISABLE, {
      ...options,
      can_disable: false,
    })).toBe('medium')
  })

  it('falls back to the default when nothing is stored', () => {
    expect(reconcileStoredEffort('', options)).toBe('medium')
  })

  it('clears the value for a model with no thinking concept', () => {
    expect(reconcileStoredEffort('high', { supported: false })).toBe('')
  })
})
