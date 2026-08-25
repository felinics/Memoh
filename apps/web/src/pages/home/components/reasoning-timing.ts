import { processBlockKey } from './process-block-key'

// The live stream doesn't carry the server's persisted reasoning duration yet.
// Measure it while streaming and retain it by the stable message/block render
// key. Once a settled/history block has reasoning_timing, the component treats
// that server value as authoritative; this store remains the compatibility
// fallback for legacy rows and older servers.
//
// Measurement is driven centrally from message-item so it covers *every*
// reasoning block, not only the last (tail) one: `markReasoningSeen` stamps a
// start the first time a block appears mid-stream, and `finalizeReasoning`
// closes it out once a later block appears or the turn ends. This is why a
// reasoning step that's immediately followed by a tool call still gets a real
// "Thought for Ns" instead of a bare "Thought".
const MAX_TIMINGS = 2048

interface ReasoningTiming {
  startedAt?: number
  durationMs?: number
}

interface ReasoningBlockIdentity {
  type: string
  id: number
}

export function createReasoningTimingStore(
  now: () => number = Date.now,
  maxEntries = MAX_TIMINGS,
) {
  const timings = new Map<string, ReasoningTiming>()

  // Desktop and browser tabs can live for days; retaining recent completed
  // turns is useful, but retaining every turn forever is not.
  function retain(key: string, timing: ReasoningTiming): void {
    timings.delete(key)
    timings.set(key, timing)
    while (timings.size > maxEntries) {
      const oldest = timings.keys().next().value
      if (oldest === undefined) break
      timings.delete(oldest)
    }
  }

  return {
    markSeen(messageId: string, block: ReasoningBlockIdentity): void {
      const key = processBlockKey(messageId, block)
      if (!key || timings.has(key)) return
      retain(key, { startedAt: now() })
    },

    finalize(messageId: string, block: ReasoningBlockIdentity): void {
      const key = processBlockKey(messageId, block)
      if (!key) return
      const timing = timings.get(key)
      if (timing?.startedAt === undefined || timing.durationMs !== undefined) return
      retain(key, { durationMs: Math.max(1, now() - timing.startedAt) })
    },

    getDuration(messageId: string, block: ReasoningBlockIdentity): number {
      const key = processBlockKey(messageId, block)
      return key ? timings.get(key)?.durationMs ?? 0 : 0
    },
  }
}

const timingStore = createReasoningTimingStore()

// Stamp the moment a reasoning block first appears while streaming. No-op once
// it already has a start or a finalized duration.
export function markReasoningSeen(messageId: string, block: ReasoningBlockIdentity): void {
  timingStore.markSeen(messageId, block)
}

// Close out a reasoning block (a later block appeared, or the turn ended),
// converting its start stamp into a final duration. Floored at 1ms so the label
// rounds to at least 1s rather than showing nothing.
export function finalizeReasoning(messageId: string, block: ReasoningBlockIdentity): void {
  timingStore.finalize(messageId, block)
}

export function getReasoningDuration(messageId: string, block: ReasoningBlockIdentity): number {
  return timingStore.getDuration(messageId, block)
}
