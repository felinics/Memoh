import { ref, type Ref } from 'vue'
import {
  fetchSession,
  fetchSessions,
  type BotSessionActivityEvent,
  type SessionSummary,
} from '@/composables/api/useChat'

export function createSessionActivity(deps: {
  currentBotId: Ref<string | null>
  sessionId: Ref<string | null>
  userScopeGeneration: () => number
  currentSessionListRevision: () => number
  currentSelectRequest: () => number
  knownSession: (sessionId: string) => SessionSummary | null | undefined
  rememberSession: (session: SessionSummary) => void
  sessionsCursor: Ref<string | null>
  hasMoreSessions: Ref<boolean>
  loadingMoreSessions: Ref<boolean>
  appendSessions: (sessions: SessionSummary[]) => void
  hasListedSession: (sessionId: string) => boolean
  touchKnownSession: (
    sessionId: string,
    updatedAt?: string,
  ) => { source: string; visibleInRecents?: boolean }
  updateKnownSessionTitle: (sessionId: string, title: string) => void
  refreshSessionsList: (botId: string) => Promise<void>
}) {
  const visibleSummaryRequests = new Map<string, Promise<SessionSummary | null>>()
  const compactingSessions = ref<Record<string, string[]>>({})
  const manualCompactions = ref(new Map<string, symbol>())
  let loadMoreRequestVersion = 0

  function isSessionCompacting(botId: string, sessionId: string): boolean {
    return manualCompactions.value.has(`${botId}\u0000${sessionId}`)
      || (compactingSessions.value[botId]?.includes(sessionId) ?? false)
  }

  function beginSessionCompaction(botId: string, sessionId: string): (() => void) | null {
    if (!botId || !sessionId || isSessionCompacting(botId, sessionId)) return null
    const key = `${botId}\u0000${sessionId}`
    const request = Symbol()
    manualCompactions.value.set(key, request)
    return () => {
      if (manualCompactions.value.get(key) === request) manualCompactions.value.delete(key)
    }
  }

  async function ensureSessionSummary(
    botId: string,
    sessionId: string,
    requestId?: number,
  ): Promise<SessionSummary | null> {
    const bid = botId.trim()
    const sid = sessionId.trim()
    if (!bid || !sid) return null
    const known = deps.knownSession(sid)
    if (known) return known

    try {
      const fetched = await fetchSession(bid, sid)
      if (
        requestId !== undefined
        && deps.currentSelectRequest() !== requestId
      ) return null
      if (
        (deps.currentBotId.value ?? '').trim() !== bid
        || (deps.sessionId.value ?? '').trim() !== sid
      ) return null
      deps.rememberSession(fetched)
      return fetched
    } catch {
      return null
    }
  }

  function ensureVisibleSessionSummary(
    botId: string,
    sessionId: string,
  ): Promise<SessionSummary | null> {
    const bid = botId.trim()
    const sid = sessionId.trim()
    if (!bid || !sid) return Promise.resolve(null)
    const known = deps.knownSession(sid)
    if (known) return Promise.resolve(known)
    const key = `${bid}\u0000${sid}`
    const pending = visibleSummaryRequests.get(key)
    if (pending) return pending
    const generation = deps.userScopeGeneration()
    const request: Promise<SessionSummary | null> = (async () => {
      try {
        const fetched = await fetchSession(bid, sid)
        if (
          generation !== deps.userScopeGeneration()
          || (deps.currentBotId.value ?? '').trim() !== bid
        ) return null
        deps.rememberSession(fetched)
        return fetched
      } catch {
        return null
      }
    })()
    visibleSummaryRequests.set(key, request)
    void request.finally(() => {
      if (visibleSummaryRequests.get(key) === request) {
        visibleSummaryRequests.delete(key)
      }
    })
    return request
  }

  async function loadMoreSessions() {
    if (!deps.hasMoreSessions.value || deps.loadingMoreSessions.value) return
    const botId = (deps.currentBotId.value ?? '').trim()
    const cursor = deps.sessionsCursor.value
    if (!botId || !cursor) return
    const userGeneration = deps.userScopeGeneration()
    const listRevision = deps.currentSessionListRevision()
    const requestVersion = ++loadMoreRequestVersion
    deps.loadingMoreSessions.value = true
    try {
      const response = await fetchSessions(botId, { cursor })
      if (
        requestVersion !== loadMoreRequestVersion
        || userGeneration !== deps.userScopeGeneration()
        || (deps.currentBotId.value ?? '').trim() !== botId
        || deps.sessionsCursor.value !== cursor
        || deps.currentSessionListRevision() !== listRevision
      ) return
      deps.appendSessions(response.items)
      deps.sessionsCursor.value = response.nextCursor
      deps.hasMoreSessions.value = response.nextCursor !== null
    } catch (error) {
      console.error('Failed to load more sessions:', error)
    } finally {
      if (requestVersion === loadMoreRequestVersion) {
        deps.loadingMoreSessions.value = false
      }
    }
  }

  function handleActivity(botId: string, event: BotSessionActivityEvent) {
    if (event.type === 'session_compaction') {
      compactingSessions.value[botId] = event.session_ids
      return
    }
    if (event.type === 'ping') return
    if (event.type === 'dropped') {
      void deps.refreshSessionsList(botId)
      return
    }
    if (event.type === 'session_touched') {
      const sessionId = event.session_id.trim()
      if (!sessionId) return
      const touched = deps.touchKnownSession(sessionId, event.updated_at)
      if (touched.source === 'listed') return
      if (touched.source === 'remembered') {
        if (touched.visibleInRecents) void deps.refreshSessionsList(botId)
        return
      }
      void deps.refreshSessionsList(botId)
      return
    }
    if (event.type === 'session_title_changed') {
      const sessionId = event.session_id.trim()
      const title = event.title.trim()
      if (sessionId && title) deps.updateKnownSessionTitle(sessionId, title)
      return
    }
    const sessionId = event.session_id.trim()
    if (sessionId && !deps.hasListedSession(sessionId)) {
      void deps.refreshSessionsList(botId)
    }
  }

  return {
    ensureSessionSummary,
    ensureVisibleSessionSummary,
    loadMoreSessions,
    handleActivity,
    isSessionCompacting,
    beginSessionCompaction,
    reset: () => {
      compactingSessions.value = {}
      manualCompactions.value.clear()
      visibleSummaryRequests.clear()
      loadMoreRequestVersion += 1
      deps.loadingMoreSessions.value = false
    },
  }
}
