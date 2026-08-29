import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createFreshnessTicker } from './freshness-ticker'

describe('createFreshnessTicker', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  function setup(overrides: {
    eligible?: () => boolean
    turnActive?: () => boolean
    intervalMs?: number
  } = {}) {
    const tick = vi.fn()
    const ticker = createFreshnessTicker({
      eligible: overrides.eligible ?? (() => true),
      turnActive: overrides.turnActive ?? (() => true),
      tick,
      intervalMs: overrides.intervalMs ?? 5000,
    })
    return { tick, ticker }
  }

  it('ticks on the interval while eligible and a turn is active', () => {
    const { tick, ticker } = setup()
    ticker.evaluate()
    expect(tick).not.toHaveBeenCalled()
    vi.advanceTimersByTime(5000)
    expect(tick).toHaveBeenCalledTimes(1)
    vi.advanceTimersByTime(10_000)
    expect(tick).toHaveBeenCalledTimes(3)
  })

  it('does not tick while no turn is active', () => {
    const { tick, ticker } = setup({ turnActive: () => false })
    ticker.evaluate()
    vi.advanceTimersByTime(60_000)
    expect(tick).not.toHaveBeenCalled()
  })

  it('fires one immediate tick when eligibility is regained', () => {
    let eligible = false
    const { tick, ticker } = setup({ eligible: () => eligible, turnActive: () => false })
    ticker.evaluate()
    expect(tick).not.toHaveBeenCalled()
    eligible = true
    ticker.evaluate()
    expect(tick).toHaveBeenCalledTimes(1)
    vi.advanceTimersByTime(60_000)
    expect(tick).toHaveBeenCalledTimes(1)
  })

  it('does not fire a regain tick on the first evaluation', () => {
    const { tick, ticker } = setup({ turnActive: () => false })
    ticker.evaluate()
    expect(tick).not.toHaveBeenCalled()
  })

  it('stops interval ticking when eligibility is lost', () => {
    let eligible = true
    const { tick, ticker } = setup({ eligible: () => eligible })
    ticker.evaluate()
    vi.advanceTimersByTime(5000)
    expect(tick).toHaveBeenCalledTimes(1)
    eligible = false
    ticker.evaluate()
    vi.advanceTimersByTime(60_000)
    expect(tick).toHaveBeenCalledTimes(1)
  })

  it('stops interval ticking when the turn ends', () => {
    let turnActive = true
    const { tick, ticker } = setup({ turnActive: () => turnActive })
    ticker.evaluate()
    vi.advanceTimersByTime(5000)
    expect(tick).toHaveBeenCalledTimes(1)
    turnActive = false
    ticker.evaluate()
    vi.advanceTimersByTime(60_000)
    expect(tick).toHaveBeenCalledTimes(1)
  })

  it('stop() halts all ticking', () => {
    const { tick, ticker } = setup()
    ticker.evaluate()
    ticker.stop()
    vi.advanceTimersByTime(60_000)
    expect(tick).not.toHaveBeenCalled()
  })
})
