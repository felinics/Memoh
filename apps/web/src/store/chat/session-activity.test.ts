import { ref } from 'vue'
import { describe, expect, it, vi } from 'vitest'
import { createSessionActivity } from './session-activity'

vi.mock('@/composables/api/useChat', () => ({ fetchSession: vi.fn(), fetchSessions: vi.fn() }))

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
