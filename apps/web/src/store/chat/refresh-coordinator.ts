import type { Ref } from 'vue'
import type { FetchSessionsResult } from '@/composables/api/useChat'

interface ChatRefreshCoordinatorDeps {
  currentBotId: Ref<string | null>
  fetchSessions: (botId: string) => Promise<FetchSessionsResult>
  applySessionsSnapshot: (response: FetchSessionsResult) => void
}

export function createChatRefreshCoordinator({
  currentBotId,
  fetchSessions,
  applySessionsSnapshot,
}: ChatRefreshCoordinatorDeps) {
  const sessionListRequests = new Map<string, Promise<void>>()
  let scopeGeneration = 0

  function refreshSessionsList(botId: string): Promise<void> {
    const bid = botId.trim()
    if (!bid) return Promise.resolve()
    const existing = sessionListRequests.get(bid)
    if (existing) return existing

    const generation = scopeGeneration
    const promise = fetchSessions(bid)
      .then((response) => {
        if (generation !== scopeGeneration) return
        if (sessionListRequests.get(bid) !== promise) return
        if ((currentBotId.value ?? '').trim() !== bid) return
        applySessionsSnapshot(response)
      })
      .catch((error) => {
        console.error('Failed to refresh sessions:', error)
      })
      .finally(() => {
        if (sessionListRequests.get(bid) === promise) sessionListRequests.delete(bid)
      })

    sessionListRequests.set(bid, promise)
    return promise
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
