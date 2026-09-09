import { effectScope, watch, type Ref } from 'vue'
import type { QueryCache } from '@pinia/colada'

const installed = new WeakSet<QueryCache>()

// The terminal status is published before the run's lifecycle row is written,
// so one refetch can still see the previous turn's snapshot.
const FOLLOW_UP_MS = 1500

// A finished turn rewrites the context but nothing else refetches the status
// or an open inspector page. One detached watcher per query cache: every
// useSessionInfo instance shares the same store ref, and invalidation is not
// deduped by Colada, so a watcher per instance would issue one request per
// mounted surface. Watching the set covers panes that finish while another
// session is still streaming.
export function installTurnEndInvalidation(streamingSessionIds: Ref<readonly string[]>, queryCache: QueryCache) {
  if (installed.has(queryCache)) return
  installed.add(queryCache)
  const invalidate = (sessionId: string) => queryCache.invalidateQueries({
    predicate: entry => (entry.key[0] === 'session-status' || entry.key[0] === 'context-lifecycle' || entry.key[0] === 'session-compactions') && entry.key[2] === sessionId,
  })
  effectScope(true).run(() => {
    watch(streamingSessionIds, (now, prev) => {
      const still = new Set(now)
      for (const sessionId of prev ?? []) {
        if (still.has(sessionId)) continue
        invalidate(sessionId)
        setTimeout(() => invalidate(sessionId), FOLLOW_UP_MS)
      }
    })
  })
}
