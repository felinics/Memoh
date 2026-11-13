// @vitest-environment jsdom
import { ref } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createSessionActivity } from './session-activity'
import { createChatViews } from './views'
import { createEmptyRuntimeProjection } from './runtime-projection'
import type { ChatAssistantTurn } from './types'

vi.mock('@/composables/api/useChat', () => ({
  fetchSession: vi.fn(),
  fetchSessions: vi.fn(),
  fetchMessagesUI: vi.fn(async () => ({ items: [], nextCursor: null })),
  locateMessageUI: vi.fn(),
}))

function activity(options: {
  currentBotId?: string | null
  sessionId?: string | null
  knownSession?: (sessionId: string) => { id: string, bot_id: string, title: string, type?: string } | null
  refreshSessionMessages?: (botId: string, sessionId: string) => Promise<void>
} = {}) {
  return createSessionActivity({
    currentBotId: ref(options.currentBotId === undefined ? 'bot-1' : options.currentBotId),
    sessionId: ref(options.sessionId === undefined ? 'session-1' : options.sessionId),
    userScopeGeneration: () => 0, currentSessionListRevision: () => 0, currentSelectRequest: () => 0,
    knownSession: options.knownSession ?? (() => null), rememberSession: vi.fn(), sessionsCursor: ref(null),
    hasMoreSessions: ref(false), loadingMoreSessions: ref(false), appendSessions: vi.fn(),
    hasListedSession: () => false, touchKnownSession: () => ({ source: 'listed' }),
    updateKnownSessionTitle: vi.fn(), refreshSessionsList: vi.fn(async () => {}),
    refreshSessionMessages: options.refreshSessionMessages ?? vi.fn(async () => {}),
  })
}

describe('session compaction activity', () => {
  it('replaces the snapshot on completion/reconnect and scopes it to its bot and session', () => {
    const state = activity()
    state.handleActivity('bot-1', { type: 'session_compaction', session_ids: ['session-1', 'session-2'] })
    expect(state.isSessionCompacting('bot-1', 'session-1')).toBe(true)
    expect(state.isSessionCompacting('bot-2', 'session-1')).toBe(false)
    expect(state.isSessionCompacting('bot-1', 'session-3')).toBe(false)
    state.handleActivity('bot-1', { type: 'session_compaction', session_ids: ['session-2'] })
    expect(state.isSessionCompacting('bot-1', 'session-1')).toBe(false)
    expect(state.isSessionCompacting('bot-1', 'session-2')).toBe(true)
    state.handleActivity('bot-1', { type: 'session_compaction', session_ids: [] })
    expect(state.isSessionCompacting('bot-1', 'session-2')).toBe(false)
  })

  it('shows manual requests immediately, deduplicates them, and waits for both owners to settle', () => {
    const state = activity()
    const done = state.beginSessionCompaction('bot-1', 'session-1')!
    expect(state.isSessionCompacting('bot-1', 'session-1')).toBe(true)
    expect(state.beginSessionCompaction('bot-1', 'session-1')).toBeNull()
    state.handleActivity('bot-1', { type: 'session_compaction', session_ids: ['session-1'] })
    done()
    expect(state.isSessionCompacting('bot-1', 'session-1')).toBe(true)
    state.handleActivity('bot-1', { type: 'session_compaction', session_ids: [] })
    expect(state.isSessionCompacting('bot-1', 'session-1')).toBe(false)
  })

  it('does not let an old request completion clear a new request after reset', () => {
    const state = activity()
    const oldDone = state.beginSessionCompaction('bot-1', 'session-1')!
    state.reset()
    expect(state.isSessionCompacting('bot-1', 'session-1')).toBe(false)
    const done = state.beginSessionCompaction('bot-1', 'session-1')!
    oldDone()
    expect(state.isSessionCompacting('bot-1', 'session-1')).toBe(true)
    done()
    expect(state.isSessionCompacting('bot-1', 'session-1')).toBe(false)
  })
})

describe('persisted background notifications', () => {
  it('refreshes message history for notification hints without refetching for ordinary touches', async () => {
    const snapshots = ['Installing', 'Installed']
    const displayed: string[] = []
    let release: () => void = () => {}
    const firstRequest = new Promise<void>((resolve) => { release = resolve })
    const refreshed = new Promise<void>((resolve) => {
      const refresh = vi.fn(async (botId: string, sessionId: string) => {
        expect([botId, sessionId]).toEqual(['bot-1', 'session-1'])
        const snapshot = snapshots.shift()
        if (snapshot === 'Installing') await firstRequest
        displayed.push(snapshot!)
        if (snapshot === 'Installed') resolve()
      })
      const state = activity({ refreshSessionMessages: refresh })
      state.handleActivity('bot-1', { type: 'session_touched', session_id: 'session-1' })
      expect(displayed).toEqual([])
      expect(snapshots).toHaveLength(2)
      state.handleActivity('bot-1', { type: 'session_touched', session_id: 'session-1', reason: 'background_task' })
      state.handleActivity('bot-1', { type: 'session_touched', session_id: 'session-1', reason: 'background_task' })
      release()
    })
    await refreshed
    expect(displayed).toEqual(['Installing', 'Installed'])
  })
})

describe('schedule activity', () => {
  function scheduleSession(id: string) {
    return { id, bot_id: 'bot-1', title: 'Scheduled run', type: 'schedule' }
  }

  it('invalidates the bot schedule snapshot on changes and dropped frames', () => {
    const state = activity()
    state.handleActivity('bot-1', { type: 'schedule_changed', schedule_id: 'schedule-1', session_id: '' })
    expect(state.scheduleActivityRevisions.value).toEqual({ 'bot-1': 1 })

    state.handleActivity('bot-2', { type: 'schedule_changed', schedule_id: 'schedule-2', session_id: '' })
    state.handleActivity('bot-1', { type: 'dropped', count: 1 })
    expect(state.scheduleActivityRevisions.value).toEqual({ 'bot-1': 2, 'bot-2': 1 })

    state.reset()
    expect(state.scheduleActivityRevisions.value).toEqual({})
  })

  it('refreshes persisted messages when the active schedule session is touched', () => {
    const refreshSessionMessages = vi.fn(async () => {})
    const state = activity({
      knownSession: id => id === 'session-1' ? scheduleSession(id) : null,
      refreshSessionMessages,
    })

    state.handleActivity('bot-1', { type: 'session_touched', session_id: 'session-1' })
    expect(refreshSessionMessages).toHaveBeenCalledWith('bot-1', 'session-1')
  })

  it('does not immediately refresh a schedule session that is not active', () => {
    const refreshSessionMessages = vi.fn(async () => {})
    const state = activity({
      sessionId: 'session-1',
      knownSession: id => id === 'session-2' ? scheduleSession(id) : null,
      refreshSessionMessages,
    })

    state.handleActivity('bot-1', { type: 'session_touched', session_id: 'session-2' })
    expect(refreshSessionMessages).not.toHaveBeenCalled()
  })

  it('does not immediately refresh an ordinary active chat session', () => {
    const refreshSessionMessages = vi.fn(async () => {})
    const state = activity({
      knownSession: id => ({ ...scheduleSession(id), type: 'chat' }),
      refreshSessionMessages,
    })

    state.handleActivity('bot-1', { type: 'session_touched', session_id: 'session-1' })
    expect(refreshSessionMessages).not.toHaveBeenCalled()
  })
})

describe('session view staleness signals', () => {
  function activityWithStaleDeps() {
    const markSessionViewStale = vi.fn()
    const markAllSessionViewsStale = vi.fn()
    const state = createSessionActivity({
      currentBotId: ref('bot-1'), sessionId: ref('session-1'),
      userScopeGeneration: () => 0, currentSessionListRevision: () => 0, currentSelectRequest: () => 0,
      knownSession: () => null, rememberSession: vi.fn(), sessionsCursor: ref(null),
      hasMoreSessions: ref(false), loadingMoreSessions: ref(false), appendSessions: vi.fn(),
      hasListedSession: () => true, touchKnownSession: () => ({ source: 'listed' }),
      updateKnownSessionTitle: vi.fn(), refreshSessionsList: vi.fn(async () => {}),
      refreshSessionMessages: vi.fn(async () => {}),
      markSessionViewStale, markAllSessionViewsStale,
    })
    return { state, markSessionViewStale, markAllSessionViewsStale }
  }

  it('marks the touched session view stale', () => {
    const { state, markSessionViewStale } = activityWithStaleDeps()
    state.handleActivity('bot-1', { type: 'session_touched', session_id: 'session-9' })
    expect(markSessionViewStale).toHaveBeenCalledWith('bot-1', 'session-9')
  })

  it('invalidates runtime replacements before persisted message activity arrives', () => {
    const { state, markSessionViewStale, markAllSessionViewsStale } = activityWithStaleDeps()
    state.handleActivity('bot-1', { type: 'session_invalidated', session_id: 'session-9' })
    expect(markSessionViewStale).toHaveBeenCalledWith('bot-1', 'session-9')
    expect(markAllSessionViewsStale).not.toHaveBeenCalled()

    state.handleActivity('bot-1', { type: 'session_invalidated', session_id: '' })
    expect(markAllSessionViewsStale).toHaveBeenCalledWith('bot-1')
  })

  it('marks every hidden view stale when events were dropped', () => {
    const { state, markAllSessionViewsStale } = activityWithStaleDeps()
    state.handleActivity('bot-1', { type: 'dropped', count: 3 })
    expect(markAllSessionViewsStale).toHaveBeenCalledWith('bot-1')
  })
})

/**
 * Failure Modes for reconnect & foreground-resume reconciliation:
 * - FM-SA1: First `activity_ready` on startup (or post-reset) mistakenly fires reconnect reconciliation,
 *           duplicating initial session list and message history fetches.
 * - FM-SA2: Subsequent SSE `activity_ready`, prepared WS `runtime_snapshot` reconnect, or `visibilitychange`
 *           (`hidden -> visible`) fails to invalidate schedule snapshots, refresh sessions list, or reload
 *           an idle visible session's messages (or leaks `visibilitychange` listeners after `dispose()`).
 * - FM-SA3: Reconnect or foreground resume reloads REST message history while the active session has a live
 *           WS run, an outbound assistant stream, a streaming transcript turn, or a tentative first-send,
 *           clobbering live in-memory output.
 */
describe('reconnect and visibility resume reconciliation', () => {
  function setVisibility(state: DocumentVisibilityState) {
    Object.defineProperty(document, 'visibilityState', {
      configurable: true,
      get: () => state,
    })
    document.dispatchEvent(new Event('visibilitychange'))
  }

  afterEach(() => {
    setVisibility('visible')
  })

  function buildReconcileHarness() {
    const currentBotId = ref<string | null>('bot-1')
    const sessionId = ref<string | null>('session-1')
    let activeProjection = createEmptyRuntimeProjection()

    const views = createChatViews({
      currentBotId,
      sessionId,
      rememberBackgroundTask: vi.fn(),
      applyPendingBackgroundEventsToTool: vi.fn(),
      bumpFsChangedAtIfFsMutation: vi.fn(),
    })
    views.configure({
      runtimeProjection: sid => (sid === 'session-1' ? activeProjection : undefined),
      startSessionRuntime: vi.fn(),
      stopSessionRuntime: vi.fn(),
      discardDraft: vi.fn(),
      invalidateDraftCommand: vi.fn(),
      saveDraftExternalAgent: vi.fn(),
      activateDraftExternalAgent: vi.fn(),
      refreshAppliedHook: vi.fn(),
      ensureVisibleSummary: vi.fn(),
    })
    views.bindChatView('panel-chat', { botId: 'bot-1', sessionId: 'session-1', viewId: 'chat' }, true)

    const refreshSessionsList = vi.fn(async () => {})
    const refreshSessionMessages = vi.fn(async () => {})
    const state = createSessionActivity({
      currentBotId,
      sessionId,
      userScopeGeneration: () => 0,
      currentSessionListRevision: () => 0,
      currentSelectRequest: () => 0,
      knownSession: () => null,
      rememberSession: vi.fn(),
      sessionsCursor: ref(null),
      hasMoreSessions: ref(false),
      loadingMoreSessions: ref(false),
      appendSessions: vi.fn(),
      hasListedSession: () => true,
      touchKnownSession: () => ({ source: 'listed' }),
      updateKnownSessionTitle: vi.fn(),
      refreshSessionsList,
      refreshSessionMessages,
      canRefreshIdleSession: views.canRefreshIdleSession,
      reconcileOnVisibilityResume: true,
    })

    return {
      currentBotId,
      sessionId,
      views,
      state,
      refreshSessionsList,
      refreshSessionMessages,
      setProjection: (next: typeof activeProjection) => {
        activeProjection = next
      },
    }
  }

  it('ignores the initial activity_ready per bot (and after reset) while reconciling on SSE reconnect and visibility resume (FM-SA1, FM-SA2)', async () => {
    const { state, refreshSessionsList, refreshSessionMessages } = buildReconcileHarness()
    try {
      // First SSE ready frame records initial coverage without re-fetching.
      state.handleActivity('bot-1', { type: 'activity_ready', cache_invalidation: true })
      expect(state.scheduleActivityRevisions.value).toEqual({})
      expect(refreshSessionsList).not.toHaveBeenCalled()
      expect(refreshSessionMessages).not.toHaveBeenCalled()

      // Subsequent SSE ready frame after stream drop/reconnect reconciles schedule, list, and idle visible messages.
      state.handleActivity('bot-1', { type: 'activity_ready', cache_invalidation: true })
      await Promise.resolve()
      await Promise.resolve()
      expect(state.scheduleActivityRevisions.value['bot-1']).toBe(1)
      expect(refreshSessionsList).toHaveBeenCalledTimes(1)
      expect(refreshSessionsList).toHaveBeenLastCalledWith('bot-1')
      expect(refreshSessionMessages).toHaveBeenCalledTimes(1)
      expect(refreshSessionMessages).toHaveBeenLastCalledWith('bot-1', 'session-1')

      // Spurious visible event without prior hidden transition is a no-op.
      setVisibility('visible')
      expect(refreshSessionsList).toHaveBeenCalledTimes(1)

      // Real hidden -> visible resume reconciles again.
      setVisibility('hidden')
      expect(refreshSessionsList).toHaveBeenCalledTimes(1)
      setVisibility('visible')
      await Promise.resolve()
      await Promise.resolve()
      expect(state.scheduleActivityRevisions.value['bot-1']).toBe(2)
      expect(refreshSessionsList).toHaveBeenCalledTimes(2)
      expect(refreshSessionMessages).toHaveBeenCalledTimes(2)

      // After reset(), the next activity_ready is treated as fresh initial bootstrap again.
      state.reset()
      state.handleActivity('bot-1', { type: 'activity_ready', cache_invalidation: true })
      expect(refreshSessionsList).toHaveBeenCalledTimes(2)
      expect(refreshSessionMessages).toHaveBeenCalledTimes(2)
    } finally {
      state.dispose()
    }

    // After dispose(), visibility transitions no longer fire reconciliation.
    setVisibility('hidden')
    setVisibility('visible')
    expect(refreshSessionsList).toHaveBeenCalledTimes(2)
  })

  it('never reloads session messages on reconnect or foreground resume while WS run, streaming turn, outbound stream, or tentative first-send is active (FM-SA3)', () => {
    const { views, state, refreshSessionsList, refreshSessionMessages, setProjection } = buildReconcileHarness()
    try {
      state.handleActivity('bot-1', { type: 'activity_ready', cache_invalidation: true })

      // 1. Active WS runtime run (`running`): schedule + session list reconcile, messages MUST NOT reload.
      setProjection({
        ...createEmptyRuntimeProjection(),
        currentRunView: {
          run_id: 'run-live',
          turn_id: 'turn-live',
          generation: '1',
          status: 'running',
          messages: [],
          started_at: '',
          updated_at: '',
        },
      })
      state.handleActivity('bot-1', { type: 'activity_ready', cache_invalidation: true })
      setVisibility('hidden')
      setVisibility('visible')
      state.handleRuntimeSnapshotReconnected('bot-1', 'session-1', {
        hasActiveRun: true,
        alreadyRefreshedMessages: false,
      })
      expect(refreshSessionsList).toHaveBeenCalledTimes(3)
      expect(refreshSessionMessages).not.toHaveBeenCalled()
      setProjection(createEmptyRuntimeProjection())

      // 2. Streaming assistant turn in transcript: messages MUST NOT reload.
      const sessionView = views.chatViews.getSession('bot-1', 'session-1')!
      const streamingTurn: ChatAssistantTurn = {
        id: 'assistant-live',
        role: 'assistant',
        messages: [],
        timestamp: '',
        streaming: true,
      }
      sessionView.transcript.messages.push(streamingTurn)
      state.handleActivity('bot-1', { type: 'activity_ready', cache_invalidation: true })
      setVisibility('hidden')
      setVisibility('visible')
      expect(refreshSessionMessages).not.toHaveBeenCalled()
      sessionView.transcript.messages.pop()

      // 3. Registered outbound assistant stream awaiting completion: messages MUST NOT reload.
      const outboundTurn: ChatAssistantTurn = {
        id: 'assistant-outbound',
        role: 'assistant',
        messages: [],
        timestamp: '',
        streaming: false,
      }
      void views.assistantStreams.trackAssistantStream({
        invocationId: 'inv-outbound',
        assistantTurn: outboundTurn,
        botId: 'bot-1',
        sessionId: 'session-1',
      })
      state.handleActivity('bot-1', { type: 'activity_ready', cache_invalidation: true })
      setVisibility('hidden')
      setVisibility('visible')
      expect(refreshSessionMessages).not.toHaveBeenCalled()
      views.assistantStreams.resolveAssistantStream('inv-outbound')

      // 4. Tentative first-send session (admitted via session_created, not yet revealed by run_accepted):
      // messages MUST NOT reload.
      views.firstSend.begin({ botId: 'bot-1', sessionId: null, viewId: 'chat' }, 'inv-first', '', () => {})
      views.firstSend.admit('inv-first', 'session-1')
      expect(views.firstSend.isSessionTentative('session-1')).toBe(true)
      state.handleActivity('bot-1', { type: 'activity_ready', cache_invalidation: true })
      setVisibility('hidden')
      setVisibility('visible')
      expect(refreshSessionMessages).not.toHaveBeenCalled()

      // Once the tentative send settles and the session is genuinely idle, reconciliation refreshes messages.
      views.firstSend.accept('inv-first')
      views.firstSend.reveal('inv-first')
      expect(views.firstSend.isSessionTentative('session-1')).toBe(false)
      setVisibility('hidden')
      setVisibility('visible')
      expect(refreshSessionMessages).toHaveBeenCalledTimes(1)
      expect(refreshSessionMessages).toHaveBeenCalledWith('bot-1', 'session-1')
    } finally {
      state.dispose()
    }
  })
})

