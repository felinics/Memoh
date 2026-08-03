import type { Ref } from 'vue'
import type { FetchSessionsResult } from '@/composables/api/useChat'

interface ChatRefreshCoordinatorDeps {
  currentBotId: Ref<string | null>
  fetchSessions: (botId: string) => Promise<FetchSessionsResult>
  currentSessionListRevision: () => number
  applySessionsSnapshot: (response: FetchSessionsResult) => void
}

interface SessionListRefresh {
  dirty: boolean
  promise: Promise<void>
}

export function createChatRefreshCoordinator({
  currentBotId,
  fetchSessions,
  currentSessionListRevision,
  applySessionsSnapshot,
}: ChatRefreshCoordinatorDeps) {
  const sessionListRequests = new Map<string, SessionListRefresh>()
  let scopeGeneration = 0

  function refreshSessionsList(botId: string): Promise<void> {
    const bid = botId.trim()
    if (!bid) return Promise.resolve()
    const existing = sessionListRequests.get(bid)
    if (existing) {
      // A second signal means the snapshot already in flight may have started
      // before the server-side mutation. Skip it and coalesce all signals into
      // one trailing refresh.
      existing.dirty = true
      return existing.promise
    }

    const generation = scopeGeneration
    const request: SessionListRefresh = {
      dirty: false,
      promise: Promise.resolve(),
    }
    request.promise = (async () => {
      do {
        request.dirty = false
        const revision = currentSessionListRevision()
        let response: FetchSessionsResult
        try {
          response = await fetchSessions(bid)
        } catch (error) {
          console.error('Failed to refresh sessions:', error)
          return
        }
        if (
          generation !== scopeGeneration
          || sessionListRequests.get(bid) !== request
          || (currentBotId.value ?? '').trim() !== bid
        ) return
        if (request.dirty || currentSessionListRevision() !== revision) {
          request.dirty = true
          continue
        }
        applySessionsSnapshot(response)
      } while (request.dirty)
    })().finally(() => {
      if (sessionListRequests.get(bid) === request) sessionListRequests.delete(bid)
    })

    sessionListRequests.set(bid, request)
    return request.promise
  }

  function resetRefreshCoordinator() {
    scopeGeneration += 1
    sessionListRequests.clear()
  }

  return {
    refreshSessionsList,
    resetRefreshCoordinator,
  }
}
