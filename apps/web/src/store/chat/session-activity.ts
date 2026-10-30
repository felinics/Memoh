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
  refreshSessionMessages: (botId: string, sessionId: string) => Promise<void>
  // Staleness signals for cached session views (see view-registry): a touch
  // marks one view, a dropped frame means events were lost and every hidden
  // view of the bot becomes unknowable.
  markSessionViewStale?: (botId: string, sessionId: string) => void
  markAllSessionViewsStale?: (botId: string) => void
}) {
  const visibleSummaryRequests = new Map<string, Promise<SessionSummary | null>>()
  const visibleSummaryFailures = ref(new Set<string>())

  function visibleSummaryKey(botId: string, sessionId: string): string {
    return `${botId.trim()}\u0000${sessionId.trim()}`
  }

  function setVisibleSummaryFailed(key: string, failed: boolean) {
    const has = visibleSummaryFailures.value.has(key)
    if (has === failed) return
    const next = new Set(visibleSummaryFailures.value)
    if (failed) next.add(key)
    else next.delete(key)
    visibleSummaryFailures.value = next
  }

  function visibleSessionSummaryFailed(botId: string, sessionId: string): boolean {
    return visibleSummaryFailures.value.has(visibleSummaryKey(botId, sessionId))
  }
  const compactingSessions = ref<Record<string, string[]>>({})
  const scheduleActivityRevisions = ref<Record<string, number>>({})
  const manualCompactions = ref(new Map<string, symbol>())
  let loadMoreRequestVersion = 0
  const persistedSessionRefreshes = new Map<string, { pending: boolean }>()

  function refreshPersistedSession(botId: string, sessionId: string) {
    const key = `${botId}:${sessionId}`
    const existing = persistedSessionRefreshes.get(key)
    if (existing) {
      existing.pending = true
      return
    }
    const state = { pending: false }
    persistedSessionRefreshes.set(key, state)
    const generation = deps.userScopeGeneration()
    void (async () => {
      do {
        state.pending = false
        await deps.refreshSessionMessages(botId, sessionId)
      } while (state.pending && generation === deps.userScopeGeneration())
    })().catch((error) => {
      console.error('Failed to refresh persisted session:', error)
    }).finally(() => {
      if (persistedSessionRefreshes.get(key) === state) persistedSessionRefreshes.delete(key)
    })
  }

  function isSessionCompacting(botId: string, sessionId: string): boolean {
    return manualCompactions.value.has(`${botId}\u0000${sessionId}`)
      || (compactingSessions.value[botId]?.includes(sessionId) ?? false)
  }

  function markScheduleSnapshotStale(botId: string) {
    scheduleActivityRevisions.value = {
      ...scheduleActivityRevisions.value,
      [botId]: (scheduleActivityRevisions.value[botId] ?? 0) + 1,
    }
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
    const key = visibleSummaryKey(bid, sid)
    const known = deps.knownSession(sid)
    if (known) {
      setVisibleSummaryFailed(key, false)
      return known
    }
    setVisibleSummaryFailed(key, false)

    const isCurrent = () => (requestId === undefined || deps.currentSelectRequest() === requestId)
      && (deps.currentBotId.value ?? '').trim() === bid
      && (deps.sessionId.value ?? '').trim() === sid
    try {
      const fetched = await fetchSession(bid, sid)
      if (!isCurrent()) return null
      deps.rememberSession(fetched)
      setVisibleSummaryFailed(key, false)
      return fetched
    } catch {
      // Same failure state as the visible-pane path: the pane offers a retry
      // instead of leaving the send waiting on a summary that never arrives.
      if (isCurrent()) setVisibleSummaryFailed(key, true)
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
    const key = visibleSummaryKey(bid, sid)
    const known = deps.knownSession(sid)
    if (known) {
      setVisibleSummaryFailed(key, false)
      return Promise.resolve(known)
    }
    const pending = visibleSummaryRequests.get(key)
    if (pending) return pending
    setVisibleSummaryFailed(key, false)
    const generation = deps.userScopeGeneration()
    const request: Promise<SessionSummary | null> = (async () => {
      try {
        const fetched = await fetchSession(bid, sid)
        if (
          generation !== deps.userScopeGeneration()
          || (deps.currentBotId.value ?? '').trim() !== bid
        ) return null
        deps.rememberSession(fetched)
        setVisibleSummaryFailed(key, false)
        return fetched
      } catch {
        if (generation === deps.userScopeGeneration() && (deps.currentBotId.value ?? '').trim() === bid) {
          setVisibleSummaryFailed(key, true)
        }
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
    if (event.type === 'activity_ready') return
    if (event.type === 'session_invalidated') {
      const sessionId = event.session_id.trim()
      if (sessionId) deps.markSessionViewStale?.(botId, sessionId)
      else deps.markAllSessionViewsStale?.(botId)
      return
    }
    if (event.type === 'session_compaction') {
      compactingSessions.value[botId] = event.session_ids
      return
    }
    if (event.type === 'ping') return
    if (event.type === 'schedule_changed') {
      markScheduleSnapshotStale(botId)
      return
    }
    if (event.type === 'dropped') {
      deps.markAllSessionViewsStale?.(botId)
      markScheduleSnapshotStale(botId)
      void deps.refreshSessionsList(botId)
      return
    }
    if (event.type === 'session_touched') {
      const sessionId = event.session_id.trim()
      if (!sessionId) return
      deps.markSessionViewStale?.(botId, sessionId)
      const activeScheduleSession = (
        (deps.currentBotId.value ?? '').trim() === botId
        && (deps.sessionId.value ?? '').trim() === sessionId
        && (deps.knownSession(sessionId)?.type ?? '').trim() === 'schedule'
      )
      if (event.reason === 'background_task' || activeScheduleSession) {
        refreshPersistedSession(botId, sessionId)
      }
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
    visibleSessionSummaryFailed,
    loadMoreSessions,
    handleActivity,
    scheduleActivityRevisions,
    isSessionCompacting,
    beginSessionCompaction,
    reset: () => {
      compactingSessions.value = {}
      scheduleActivityRevisions.value = {}
      manualCompactions.value.clear()
      visibleSummaryRequests.clear()
      visibleSummaryFailures.value = new Set()
      persistedSessionRefreshes.clear()
      loadMoreRequestVersion += 1
      deps.loadingMoreSessions.value = false
    },
  }
}
