import { ref } from 'vue'
import { describe, expect, it, vi } from 'vitest'
import { createSessionActivity } from './session-activity'

vi.mock('@/composables/api/useChat', () => ({ fetchSession: vi.fn(), fetchSessions: vi.fn() }))

function activity() {
  return createSessionActivity({
    currentBotId: ref('bot-1'), sessionId: ref('session-1'),
    userScopeGeneration: () => 0, currentSessionListRevision: () => 0, currentSelectRequest: () => 0,
    knownSession: () => null, rememberSession: vi.fn(), sessionsCursor: ref(null),
    hasMoreSessions: ref(false), loadingMoreSessions: ref(false), appendSessions: vi.fn(),
    hasListedSession: () => false, touchKnownSession: () => ({ source: 'listed' }),
    updateKnownSessionTitle: vi.fn(), refreshSessionsList: vi.fn(async () => {}),
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
