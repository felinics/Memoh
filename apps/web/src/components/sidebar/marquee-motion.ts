// Motion math for marquee-text.vue, kept pure so it is testable without a
// DOM. The component turns the result into the CSS vars the stylesheet reads;
// everything else (transform, fades) is a pure CSS function of the single
// animated variable --marquee-pos, so nothing here can drift out of sync
// with what is painted.

// Reading pace for the scroll leg. Slow enough to read CJK comfortably,
// fast enough that a sidebar-width title completes in a few seconds.
export const MARQUEE_SPEED_PX_PER_S = 30
// The keyframe x values run from -Overshoot to +travel+Overshoot (the two
// out-of-window offsets that clamp to a constant hold at each end — see the
// component stylesheet). Both keyframe ends and this constant must change
// together, or the hold silently changes length.
export const MARQUEE_OVERSHOOT_PX = 6

export interface MarqueeMotion {
  truncated: boolean
  // Distance the text must travel left for the tail to rest flush at the
  // viewport's right edge — exactly the overflow, never more: scrolling
  // further would leave a dead gap between the tail and the actions slot.
  travelPx: number
  // ONE full cycle: forward leg (travel + 2·Overshoot at constant speed —
  // the overshoot portions clamp to the hold dwells), then the same distance
  // back. Time-based, not a fraction of the cycle: a percentage-based hold
  // made long titles dwell proportionally longer (a 400px title sat ~1.9s
  // before moving, a 30px one only 143ms — the "hover forever before it
  // scrolls" bug), because stretching the duration to keep speed constant
  // stretched the dwell with it.
  durationMs: number
}

// Sub-pixel jitter (<2px) is not truncation: a nearly-fitting label keeps
// its crisp tail rather than gaining a fade and a scroll.
export function computeMarqueeMotion(overflowPx: number): MarqueeMotion {
  if (overflowPx <= 1) {
    return { truncated: false, travelPx: 0, durationMs: 0 }
  }
  const span = overflowPx + 2 * MARQUEE_OVERSHOOT_PX
  const durationMs = Math.round((span * 2000) / MARQUEE_SPEED_PX_PER_S)
  return { truncated: true, travelPx: overflowPx, durationMs }
}
