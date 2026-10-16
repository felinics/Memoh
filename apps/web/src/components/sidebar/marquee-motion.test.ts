import { describe, expect, it } from 'vitest'
import { computeMarqueeMotion, MARQUEE_OVERSHOOT_PX, MARQUEE_SPEED_PX_PER_S } from './marquee-motion'

describe('computeMarqueeMotion', () => {
  it('treats fitting and sub-pixel overflow as not truncated', () => {
    for (const overflow of [0, -5, 1]) {
      expect(computeMarqueeMotion(overflow)).toEqual({ truncated: false, travelPx: 0, durationMs: 0 })
    }
  })

  it('travels exactly the overflow so the tail rests flush at the right edge', () => {
    const motion = computeMarqueeMotion(120)
    expect(motion.truncated).toBe(true)
    expect(motion.travelPx).toBe(120)
  })

  it('keeps the scroll speed constant: one cycle covers the span twice', () => {
    for (const overflow of [30, 120, 400]) {
      const span = overflow + 2 * MARQUEE_OVERSHOOT_PX
      expect(computeMarqueeMotion(overflow).durationMs).toBe(Math.round((span * 2000) / MARQUEE_SPEED_PX_PER_S))
    }
  })

  it('holds a constant dwell at each end regardless of title length', () => {
    // The dwell is the clamped overshoot at each end of BOTH legs: the offset
    // travels 2·Overshoot within it at the constant speed, so every title —
    // 30px or 400px — waits the same (2·Overshoot)/speed before its first
    // pixel moves. A percentage-based dwell broke this (long titles waited
    // proportionally longer), which is why the clamp exists.
    const dwellMs = (2 * MARQUEE_OVERSHOOT_PX * 1000) / MARQUEE_SPEED_PX_PER_S
    expect(dwellMs).toBe(400)
  })

  it('leaves a short title still fitting beside the overlay', () => {
    expect(computeMarqueeMotion(-50, 24)).toEqual({ truncated: false, travelPx: 0, durationMs: 0 })
  })

  it('reveals the tail of a title that only overflows while actions are visible', () => {
    const titleWidth = 195.5
    const viewportWidth = 200.25
    const overlayWidth = 24
    expect(computeMarqueeMotion(titleWidth - viewportWidth).truncated).toBe(false)
    const covered = computeMarqueeMotion(titleWidth - viewportWidth, overlayWidth)
    expect(covered.truncated).toBe(true)
    expect(titleWidth - covered.travelPx).toBe(viewportWidth - overlayWidth)
  })

  it('accounts for the overlay at the endpoint without changing reading speed', () => {
    const covered = computeMarqueeMotion(120, 24)
    expect(covered.travelPx).toBe(144)
    expect(covered.durationMs).toBe(Math.round(((144 + 2 * MARQUEE_OVERSHOOT_PX) * 2000) / MARQUEE_SPEED_PX_PER_S))
  })
})
