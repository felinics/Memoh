import type { ReasoningOptions } from '@memohai/sdk'
import { describe, expect, it } from 'vitest'
import {
  REASONING_EFFORT_DISABLE,
  REASONING_EFFORT_LEGACY_OFF,
  reconcileStoredEffort,
  selectableEfforts,
} from './reasoning-effort'

// The capability questions these used to answer — which tiers a model offers,
// whether off is reachable, how max normalizes — now live in internal/reasoning
// and arrive resolved on the model's `reasoning` field. What is left to test is
// rendering the answer and migrating a stored value, which is all this file does.

// Tiers stay literals: they are the server's answer arriving over the wire, not a
// vocabulary this module owns. The two tokens it does own are named — the off
// value it writes back, and the legacy spelling it still has to read.
const TIERS = ['low', 'medium', 'high']
const DEFAULT_TIER = 'medium'

function options(overrides: Partial<ReasoningOptions> = {}): ReasoningOptions {
  return {
    supported: true,
    can_disable: true,
    efforts: TIERS,
    default_effort: DEFAULT_TIER,
    ...overrides,
  }
}

const NO_THINKING: ReasoningOptions = { supported: false }

describe('selectableEfforts', () => {
  it('offers off at the front when the server says off is reachable', () => {
    expect(selectableEfforts(options())).toEqual([REASONING_EFFORT_DISABLE, ...TIERS])
  })

  it('offers no off for a model that cannot be turned off', () => {
    expect(selectableEfforts(options({ can_disable: false }))).toEqual(TIERS)
  })

  it('offers nothing for a model with no thinking concept', () => {
    expect(selectableEfforts(NO_THINKING)).toEqual([])
  })

  it('offers nothing when the field is absent, as it is for non-chat models', () => {
    expect(selectableEfforts(undefined)).toEqual([])
    expect(selectableEfforts(null)).toEqual([])
  })
})

describe('reconcileStoredEffort', () => {
  it('keeps a tier the model still offers', () => {
    expect(reconcileStoredEffort('high', options())).toBe('high')
  })

  it('lands a stranded tier on the model default', () => {
    expect(reconcileStoredEffort('xhigh', options())).toBe(DEFAULT_TIER)
  })

  it('keeps off when off is still reachable', () => {
    expect(reconcileStoredEffort(REASONING_EFFORT_DISABLE, options())).toBe(REASONING_EFFORT_DISABLE)
  })

  it('reads the legacy off spelling as off', () => {
    expect(reconcileStoredEffort(REASONING_EFFORT_LEGACY_OFF, options())).toBe(REASONING_EFFORT_DISABLE)
  })

  it('moves off to the default when the new model cannot be turned off', () => {
    expect(reconcileStoredEffort(REASONING_EFFORT_DISABLE, options({ can_disable: false }))).toBe(DEFAULT_TIER)
  })

  it('falls back to the default when nothing is stored', () => {
    expect(reconcileStoredEffort('', options())).toBe(DEFAULT_TIER)
  })

  it('clears the value for a model with no thinking concept', () => {
    expect(reconcileStoredEffort('high', NO_THINKING)).toBe('')
  })

  it('keeps a dormant preference for an always-on model with no controls', () => {
    const alwaysOn = options({
      can_disable: false,
      efforts: [],
      default_effort: undefined,
    })
    expect(reconcileStoredEffort('high', alwaysOn)).toBe('high')
    expect(reconcileStoredEffort('none', alwaysOn)).toBe('disable')
  })
})
