import { effectScope, watch, type Ref } from 'vue'
import type { QueryCache } from '@pinia/colada'

const installed = new WeakSet<QueryCache>()

// A finished turn rewrites the context but nothing else refetches the status
// or an open inspector page. One detached watcher per query cache: every
// useSessionInfo instance shares the same store ref, and invalidation is not
// deduped by Colada, so a watcher per instance would issue one request per
// mounted surface.
export function installTurnEndInvalidation(streamingSessionId: Ref<string | null | undefined>, queryCache: QueryCache) {
  if (installed.has(queryCache)) return
  installed.add(queryCache)
  effectScope(true).run(() => {
    watch(streamingSessionId, (now, prev) => {
      if (!prev || prev === now) return
      queryCache.invalidateQueries({
        predicate: entry => (entry.key[0] === 'session-status' || entry.key[0] === 'context-lifecycle') && entry.key[2] === prev,
      })
    })
  })
}
