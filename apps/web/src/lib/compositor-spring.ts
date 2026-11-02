// Spring motion that runs on the compositor.
//
// Ported from the mobile interaction prototype (native-web-lab index.html,
// "Spring models" / "Compositor spring engine"), where these values were tuned
// on a real phone. Logic is unchanged; only TypeScript types were added.
//
// The spring is solved ahead of time at 120 Hz and handed to element.animate()
// as linear keyframes, so only transform / opacity run on the compositor and
// nothing animates from a requestAnimationFrame loop on the main thread. A
// gesture can interrupt a running spring: sampleMidAirIfRunning() reads the
// position and velocity the curve had reached and continues from there.

export interface SpringConfig {
  stiffness: number
  damping: number
  mass: number
  onRest?: () => void
}

// SwiftUI perceptual spring (WWDC23 "Animate with springs"):
//   k = (2π / duration)² · m,  ζ = bounce ≥ 0 ? 1 − bounce : 1 / (1 + bounce),  c = 2ζ·√(k·m)
export function springFromPerceptual(duration: number, bounce = 0, mass = 1): SpringConfig {
  const d = Math.max(0.08, duration)
  const b = Math.max(-0.95, Math.min(0.95, bounce))
  const omega = (2 * Math.PI) / d
  const stiffness = omega * omega * mass
  const zeta = b >= 0 ? 1 - b : 1 / (1 + b)
  return { stiffness, damping: 2 * zeta * Math.sqrt(stiffness * mass), mass }
}

// Page switches (list ⇄ chat) and sheets. Chosen on device in the prototype
// over a slower critically damped spring and a bouncier one.
export const PAGE_SPRING: SpringConfig = { stiffness: 500, damping: 50, mass: 0.8 }
// Bubble scale and small elements returning to rest.
export const BUBBLE_SPRING: SpringConfig = springFromPerceptual(0.26, 0.18)

const clamp01 = (v: number) => Math.max(0, Math.min(1, v))

// iOS-style resistance past an edge: the further the drag, the less it moves.
export function rubberBand(distance: number, dimension: number, constant = 0.55) {
  if (dimension <= 0) return 0
  const sign = distance < 0 ? -1 : 1
  const abs = Math.abs(distance)
  return sign * (1 - 1 / ((abs * constant) / dimension + 1)) * dimension
}

// Release velocity from the last ~85ms of pointer samples, in px/s.
export class VelocityTracker {
  private samples: { pos: number, time: number }[] = []

  reset() {
    this.samples.length = 0
  }

  add(pos: number, time = performance.now()) {
    this.samples.push({ pos, time })
    const cutoff = time - 85
    while (this.samples.length > 2 && this.samples[0]!.time < cutoff) this.samples.shift()
  }

  getVelocity() {
    if (this.samples.length < 2) return 0
    const first = this.samples[0]!
    const last = this.samples[this.samples.length - 1]!
    const dt = (last.time - first.time) / 1000
    return dt > 0.001 ? (last.pos - first.pos) / dt : 0
  }
}

interface SpringCurve {
  values: number[]
  velocities: number[]
  durationMs: number
}

function solveSpringCurve(from: number, to: number, initialVelocity: number, config: Partial<SpringConfig> = {}): SpringCurve {
  const m = config.mass ?? 1
  const k = (config.stiffness ?? 380) / m
  const c = (config.damping ?? 32) / m
  const dt = 1 / 120
  const substeps = 2
  const h = dt / substeps
  const values = [from]
  const velocities = [initialVelocity]

  let x = from
  let v = initialVelocity
  const span = Math.max(0.05, Math.abs(to - from))
  const restDelta = span * 0.0012
  const restSpeed = span * 0.008

  for (let frame = 0; frame < 108; frame++) {
    for (let s = 0; s < substeps; s++) {
      const a = -k * (x - to) - c * v
      v += a * h
      x += v * h
    }
    values.push(x)
    velocities.push(v)
    if (Math.abs(x - to) <= restDelta && Math.abs(v) <= restSpeed && frame > 6) break
  }
  values[values.length - 1] = to
  velocities[velocities.length - 1] = 0
  return { values, velocities, durationMs: (values.length - 1) * (1000 / 120) }
}

export interface SpringBinding {
  el: HTMLElement | null | undefined
  prop: 'transform' | 'opacity'
  format: (value: number) => string
}

export class CompositorSpring {
  current: number
  velocity = 0
  private activeAnims: Animation[] = []
  private curve: SpringCurve | null = null
  private startTime = 0

  // bindingsFactory returns the elements the value drives. Each binding should
  // own its property on its element; two springs writing the same element's
  // transform fight.
  private bindingsFactory: () => SpringBinding[]

  constructor(initialValue: number, bindingsFactory: () => SpringBinding[]) {
    this.current = initialValue
    this.bindingsFactory = bindingsFactory
  }

  sampleMidAirIfRunning() {
    if (!this.curve || this.activeAnims.length === 0) return
    const elapsed = performance.now() - this.startTime
    const progress = clamp01(elapsed / Math.max(1, this.curve.durationMs))
    const idxFloat = progress * (this.curve.values.length - 1)
    const i0 = Math.floor(idxFloat)
    const i1 = Math.min(this.curve.values.length - 1, i0 + 1)
    const frac = idxFloat - i0
    const values = this.curve.values
    const velocities = this.curve.velocities
    this.current = values[i0]! + (values[i1]! - values[i0]!) * frac
    this.velocity = velocities[i0]! + (velocities[i1]! - velocities[i0]!) * frac
    this.cancelAnims()
  }

  cancelAnims() {
    for (const a of this.activeAnims) {
      try {
        a.cancel()
      }
      catch {}
    }
    this.activeAnims.length = 0
    this.curve = null
  }

  private applyInline(val: number) {
    for (const b of this.bindingsFactory()) {
      if (b.el) b.el.style[b.prop] = b.format(val)
    }
  }

  setImmediate(val: number, vel = 0) {
    this.cancelAnims()
    this.current = val
    this.velocity = vel
    this.applyInline(val)
  }

  springTo(target: number, initialVelocity: number | null = null, config: Partial<SpringConfig> = {}) {
    this.sampleMidAirIfRunning()
    const v0 = initialVelocity !== null ? initialVelocity : this.velocity

    if (Math.abs(this.current - target) < 0.001 && Math.abs(v0) < 0.01) {
      this.setImmediate(target)
      config.onRest?.()
      return
    }

    const curve = solveSpringCurve(this.current, target, v0, config)
    this.curve = curve
    this.startTime = performance.now()
    this.current = target
    this.velocity = 0
    // The inline style holds the resting value; the running animation overrides it.
    this.applyInline(target)

    const bindings = this.bindingsFactory()
    bindings.forEach((b, idx) => {
      if (!b.el || !b.el.animate) return
      const frames = curve.values.map(v => ({ [b.prop]: b.format(v) }))
      const anim = b.el.animate(frames, { duration: curve.durationMs, easing: 'linear' })
      if (idx === 0) {
        anim.onfinish = () => {
          this.activeAnims.length = 0
          this.curve = null
          config.onRest?.()
        }
      }
      this.activeAnims.push(anim)
    })
  }
}
