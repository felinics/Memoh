import type { RuntimeTranscriptSlice } from './runtime-projection'

// Projection frames and run_accepted travel on different channels with no
// ordering guarantee. When a projection names a turnId no on-screen turn
// knows yet, while a local send is still waiting for its acceptance, the
// frame is likely the live twin of that unbound optimistic turn; applying
// it immediately would append a duplicate. Hold it in a per-turn buffer
// until the acceptance connects the names. If no binding arrives within the
// grace window — a run from another device has no local twin — flush it
// standalone under the turnId's own render identity.
export interface RuntimeSliceBufferDeps {
  hasUnboundOptimisticTurn: () => boolean
  hasTurnWithId: (turnId: string) => boolean
  applyStandalone: (slice: RuntimeTranscriptSlice) => void
}

const RUNTIME_BINDING_GRACE_MS = 750

export function createRuntimeSliceBuffer(deps: RuntimeSliceBufferDeps) {
  const pending = new Map<string, {
    slice: RuntimeTranscriptSlice
    timer: ReturnType<typeof setTimeout>
  }>()

  function flush(turnId: string) {
    const entry = pending.get(turnId)
    if (!entry) return
    pending.delete(turnId)
    clearTimeout(entry.timer)
    deps.applyStandalone(entry.slice)
  }

  function maybeBuffer(slice: RuntimeTranscriptSlice): boolean {
    if (slice.operation) return false
    if (!deps.hasUnboundOptimisticTurn()) return false
    if (deps.hasTurnWithId(slice.turnId)) return false
    const existing = pending.get(slice.turnId)
    if (existing) clearTimeout(existing.timer)
    const timer = setTimeout(() => flush(slice.turnId), RUNTIME_BINDING_GRACE_MS)
    pending.set(slice.turnId, { slice, timer })
    return true
  }

  function clear() {
    for (const entry of pending.values()) clearTimeout(entry.timer)
    pending.clear()
  }

  return { maybeBuffer, flush, clear }
}
