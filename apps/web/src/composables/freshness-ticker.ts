export interface FreshnessTickerOptions {
  eligible: () => boolean
  turnActive: () => boolean
  tick: () => void
  intervalMs?: number
}

export interface FreshnessTicker {
  evaluate: () => void
  stop: () => void
}

export const FRESHNESS_TICK_MS = 5_000

// Attention-gated freshness ticker shared by workspace file surfaces (tree,
// viewer, preview). It replaces their unconditional polling: passive refresh
// runs ONLY while the surface has the user's attention AND a turn is running
// for the current bot — the one window where the workspace can change without
// a server-pushed fs event. Regaining attention fires a single catch-up tick.
// Idle surfaces generate zero traffic, so paused cloud sandboxes stay paused.
export function createFreshnessTicker(options: FreshnessTickerOptions): FreshnessTicker {
  const intervalMs = options.intervalMs ?? FRESHNESS_TICK_MS
  let timer: ReturnType<typeof setInterval> | null = null
  let wasEligible: boolean | null = null
  let stopped = false

  function clearTimer() {
    if (timer !== null) {
      clearInterval(timer)
      timer = null
    }
  }

  function evaluate() {
    if (stopped) return
    const eligibleNow = options.eligible()
    if (eligibleNow && wasEligible === false) options.tick()
    wasEligible = eligibleNow
    const shouldRun = eligibleNow && options.turnActive()
    if (shouldRun && timer === null) {
      timer = setInterval(() => {
        if (!options.eligible() || !options.turnActive()) {
          clearTimer()
          return
        }
        options.tick()
      }, intervalMs)
    } else if (!shouldRun) {
      clearTimer()
    }
  }

  function stop() {
    stopped = true
    clearTimer()
  }

  return { evaluate, stop }
}
