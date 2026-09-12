import { afterEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import { createSessionActivity } from './session-activity'
import type { BotSessionActivityEvent, ChatWebSocket, UIStreamEvent, UITurn, UIUserTurn } from '@/composables/api/useChat'
import { createChatViews } from './views'
import { createChatRealtimeController, type ChatRealtimeTransport } from './realtime'

const api = vi.hoisted(() => ({ fetchMessagesUI: vi.fn(), locateMessageUI: vi.fn(), connectWebSocket: vi.fn(), streamBotSessionsActivityEvents: vi.fn() }))
vi.mock('@/composables/api/useChat', () => api)

async function flush() { for (let i = 0; i < 15; i++) await Promise.resolve() }
const rawUser = (id: string, text: string): UIUserTurn => ({
  role: 'user', id, turn_id: id, text, attachments: [], timestamp: '2026-09-12T00:00:00.000Z',
})

function harness() {
  api.fetchMessagesUI.mockReset().mockImplementation(async (_bot: string, sid: string) => [rawUser(`${sid}-old`, 'old')])
  const views = createChatViews({
    currentBotId: ref('bot'), sessionId: ref('a'), rememberBackgroundTask: task => task,
    applyPendingBackgroundEventsToTool: () => {}, bumpFsChangedAtIfFsMutation: () => {},
  })
  let attempt!: (signal: AbortSignal) => Promise<void>
  let activity!: (event: BotSessionActivityEvent) => void
  let closeAttempt!: () => void
  let socketEvent!: (event: UIStreamEvent) => void
  let holdSnapshots = false
  const snapshot = (sid: string) => socketEvent({
    type: 'runtime_snapshot', session_id: sid, epoch: `epoch-${sid}`, seq: 1,
    snapshot: { bot_id: 'bot', session_id: sid, epoch: `epoch-${sid}`, seq: 1, updated_at: '2026-09-12T00:00:00.000Z' },
  })
  const activityState = createSessionActivity({
    currentBotId: ref('bot'), sessionId: ref('a'), userScopeGeneration: () => 0,
    currentSessionListRevision: () => 0, currentSelectRequest: () => 0,
    knownSession: () => null, rememberSession: vi.fn(), sessionsCursor: ref(null),
    hasMoreSessions: ref(false), loadingMoreSessions: ref(false), appendSessions: vi.fn(),
    hasListedSession: () => true, touchKnownSession: () => ({ source: 'listed' }),
    updateKnownSessionTitle: vi.fn(), refreshSessionsList: vi.fn(async () => {}),
    refreshSessionMessages: views.refreshCurrentSession,
    markSessionViewStale: views.chatViews.markSessionStale,
    markAllSessionViewsStale: views.chatViews.markAllSessionsStale,
  })
  const transport: ChatRealtimeTransport = {
    createRetryingStream: () => ({ start: run => { attempt = run }, stop: () => {} }),
    streamBotSessionsActivityEvents: (_bot, _signal, handler) => {
      activity = handler
      return new Promise<void>(resolve => { closeAttempt = resolve })
    },
    connectWebSocket: (_bot, handler) => {
      socketEvent = handler
      return {
        connected: true, onOpen: null, onClose: null, close: () => {}, abort: () => {},
        send: message => {
          if (message.type !== 'runtime_subscribe') return
          if (!holdSnapshots) snapshot(message.session_id)
        },
      } as ChatWebSocket
    },
  }
  const realtime = createChatRealtimeController({
    prepareSessionRuntime: views.loadInitialMessages,
    onRuntimeProjection: (bid, sid, change) => { views.sessionTranscript(bid, sid).applyRuntimeTranscript(change.current.transcript) },
    onWebSocketEvent: () => {},
    onBotSessionsActivityEvent: activityState.handleActivity,
    onActivityStreamCoverageChanged: views.chatViews.setActivityStreamCoverage,
  }, transport)
  views.configure({
    runtimeProjection: realtime.runtimeProjection,
    startSessionRuntime: realtime.startSessionRuntime,
    stopSessionRuntime: realtime.stopSessionRuntime,
    discardDraft: () => {}, invalidateDraftCommand: () => {}, saveDraftExternalAgent: () => {},
    activateDraftExternalAgent: () => {}, refreshAppliedHook: () => {}, ensureVisibleSummary: () => {},
  })
  realtime.startWebSocket('bot')
  realtime.startBotSessionsActivityStream('bot')
  const openActivity = () => attempt(new AbortController().signal)
  const select = (sid: string) => views.bindChatView('panel', { botId: 'bot', sessionId: sid, viewId: 'panel' }, true)
  return {
    views, realtime, select, openActivity,
    activity: (event: BotSessionActivityEvent) => activity(event),
    closeActivity: () => closeAttempt(),
    holdSnapshots: () => { holdSnapshots = true },
    socketEvent: (event: UIStreamEvent) => socketEvent(event),
  }
}

afterEach(() => vi.useRealTimers())

describe('cached revisit activity coverage', () => {
  it('shows untouched cache immediately while covered, then atomically revalidates it', async () => {
    const h = harness()
    const active = h.openActivity()
    h.activity({ type: 'activity_ready', cache_invalidation: true })
    const a = h.select('a')
    await flush()
    h.select('b')
    await flush()
    h.holdSnapshots()
    let resolveHistory!: (turns: UITurn[]) => void
    api.fetchMessagesUI.mockImplementationOnce(() => new Promise<UITurn[]>(resolve => { resolveHistory = resolve }))
    h.select('a')
    expect(a.transcript.loadingMessages.value).toBe(false)
    expect(a.transcript.visibleMessages.value.map(turn => turn.id)).toEqual(['a-old'])
    resolveHistory([rawUser('a-new', 'new')])
    await flush()
    expect(a.transcript.visibleMessages.value.map(turn => turn.id)).toEqual(['a-old'])
    h.socketEvent({ type: 'runtime_snapshot', session_id: 'a', epoch: 'epoch-a', seq: 2,
      snapshot: { bot_id: 'bot', session_id: 'a', epoch: 'epoch-a', seq: 2, updated_at: '2026-09-12T00:01:00Z' },
    })
    await flush()
    expect(a.transcript.visibleMessages.value.map(turn => turn.id)).toEqual(['a-new'])
    h.closeActivity()
    await active
    h.realtime.stopStreams()
  })

  it('keeps a refreshed cache conservative throughout an activity outage and its recovery', async () => {
    const h = harness()
    const active = h.openActivity()
    h.activity({ type: 'activity_ready', cache_invalidation: true })
    const a = h.select('a')
    await flush()
    h.select('b')
    await flush()
    h.closeActivity()
    await active
    const reconnect = h.openActivity()
    expect(a.staleWhileHidden).toBe(true)
    h.select('a')
    expect(a.transcript.loadingMessages.value).toBe(true)
    await flush()
    expect(a.staleWhileHidden).toBe(false)
    h.select('b')
    await flush()
    let resolveHistory!: (turns: UITurn[]) => void
    api.fetchMessagesUI.mockImplementationOnce(() => new Promise<UITurn[]>(resolve => { resolveHistory = resolve }))
    h.select('a')
    expect(a.transcript.visibleMessages.value).toEqual([])
    resolveHistory([rawUser('a-edited', 'edited')])
    await flush()
    expect(a.staleWhileHidden).toBe(false)
    h.select('b')
    await flush()
    // Ready cannot certify writes missed after the successful REST fetch.
    h.activity({ type: 'activity_ready', cache_invalidation: true })
    expect(a.staleWhileHidden).toBe(true)
    h.select('a')
    expect(a.transcript.visibleMessages.value).toEqual([])
    await flush()
    expect(a.staleWhileHidden).toBe(false)
    h.select('b')
    await flush()
    h.select('a')
    expect(a.transcript.loadingMessages.value).toBe(false)
    await flush()
    h.closeActivity()
    await reconnect
    h.realtime.stopStreams()
  })

  it.each(['no-frame', 'legacy', 'unsupported'] as const)('does not trust cached views with %s activity coverage', async (mode) => {
    vi.useFakeTimers({ now: Date.parse('2026-09-12T00:00:00Z') })
    const h = harness()
    const active = h.openActivity()
    if (mode === 'legacy') h.activity({ type: 'ping' })
    if (mode === 'unsupported') h.activity({ type: 'activity_ready', cache_invalidation: false })
    const a = h.select('a')
    await flush()
    h.select('b')
    await flush()
    await vi.advanceTimersByTimeAsync(60_000)
    h.holdSnapshots()
    h.select('a')
    expect(a.transcript.visibleMessages.value).toEqual([])
    h.closeActivity()
    await active
    h.realtime.stopStreams()
    await flush()
  })

  it.each([true, false])('masks an active cross-client edit (invalidation support: %s)', async (supported) => {
    const h = harness()
    const active = h.openActivity()
    if (supported) h.activity({ type: 'activity_ready', cache_invalidation: true })
    else h.activity({ type: 'ping' })
    const a = h.select('a')
    await flush()
    h.select('b')
    await flush()
    // The server's admission observer now emits this before edit/retry
    // generation. Legacy servers have no such signal and remain masked.
    if (supported) h.activity({ type: 'session_invalidated', session_id: 'a' })
    h.holdSnapshots()
    h.select('a')
    await flush()
    expect(a.transcript.visibleMessages.value).toEqual([])
    h.socketEvent({
      type: 'runtime_snapshot', session_id: 'a', epoch: 'epoch-a', seq: 2,
      snapshot: {
        bot_id: 'bot', session_id: 'a', epoch: 'epoch-a', seq: 2, updated_at: '2026-09-12T00:01:00Z',
        current_run_view: {
          run_id: 'edit-run', turn_id: 'edit-turn', generation: 'generation-a', status: 'running',
          operation: { kind: 'edit', replace_from_message_id: 'a-old', replacement_user_turn: rawUser('edit-turn', 'edited') },
          messages: [], started_at: '2026-09-12T00:01:00Z', updated_at: '2026-09-12T00:01:00Z',
        },
      },
    })
    await flush()
    expect(a.transcript.visibleMessages.value.map(turn => turn.id)).not.toContain('a-old')
    expect(a.transcript.visibleMessages.value[0]).toMatchObject({ role: 'user', text: 'edited' })
    h.closeActivity()
    await active
    h.realtime.stopStreams()
  })
})
