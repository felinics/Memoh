import { effectScope, watch, type Ref } from 'vue'
import type { QueryCache } from '@pinia/colada'

const installed = new WeakSet<QueryCache>()

// Marks are written by tool calls during a turn, so the list is refreshed
// when a session stops streaming (the same moment its status is refreshed).
// Installed once per query cache, like the session-status invalidation.
export function installGuiMarksInvalidation(streamingSessionIds: Ref<readonly string[]>, queryCache: QueryCache) {
  if (installed.has(queryCache)) return
  installed.add(queryCache)
  const invalidate = (sessionId: string) => queryCache.invalidateQueries({
    predicate: entry => entry.key[0] === 'gui-marks' && entry.key[2] === sessionId,
  })
  effectScope(true).run(() => {
    watch(streamingSessionIds, (now, prev) => {
      const still = new Set(now)
      for (const sessionId of prev ?? []) {
        if (still.has(sessionId)) continue
        invalidate(sessionId)
      }
    })
  })
}
