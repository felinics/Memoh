import { ref } from 'vue'
import { describe, expect, it, vi } from 'vitest'
import { createRuntimeIntegration, wsFrameErrorCode, type RuntimeIntegrationDeps } from './runtime-integration'
import { failureStage } from './send'
import { createEmptyRuntimeProjection } from './runtime-projection'
import { createChatRealtimeController } from './realtime'
import { createSessionActivity } from './session-activity'
import { createChatViews } from './views'
import type { ChatWebSocket, RuntimeCurrentRunView, UIStreamEvent } from '@/composables/api/useChat'

vi.mock('@/composables/api/useChat', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/composables/api/useChat')>()
  return {
    ...actual,
    fetchMessagesUI: vi.fn(async () => ({ items: [], nextCursor: null })),
    locateMessageUI: vi.fn(),
    fetchSession: vi.fn(),
    fetchSessions: vi.fn(),
  }
})

it('模式保存的所有帧均不刷新历史或改写上轮回复', () => {
  const applyRuntimeTranscript = vi.fn()
  const refreshCurrentSession = vi.fn()
  const resyncRuntimeTranscript = vi.fn()
  const deps = {
    bumpProjectionVersion: vi.fn(),
    chatViews: { getSession: () => ({ transcript: { applyRuntimeTranscript } }) },
    decisions: { observeRun: vi.fn() },
    refreshCurrentSession,
    resyncRuntimeTranscript,
  } as unknown as RuntimeIntegrationDeps
  const integration = createRuntimeIntegration(deps)
  const event = {
    type: 'runtime_snapshot' as const, session_id: 'session', epoch: 'epoch', seq: 1,
    snapshot: { bot_id: 'bot', session_id: 'session', epoch: 'epoch', seq: 1, updated_at: '' },
  }
  let previous = createEmptyRuntimeProjection()
  for (const status of ['admitting', 'running', 'finishing', 'completed'] as const) {
    const run: RuntimeCurrentRunView = {
      run_id: 'config', turn_id: 'config-turn', generation: '1', status,
      configuration_only: true, messages: [], started_at: '', updated_at: '',
    }
    const current = { ...createEmptyRuntimeProjection(), currentRunView: run }
    integration.handleProjection('bot', 'session', { previous, current, event })
    previous = current
  }
  integration.handleProjection('bot', 'session', { previous, current: createEmptyRuntimeProjection(), event })
  expect(applyRuntimeTranscript).not.toHaveBeenCalled()
  expect(refreshCurrentSession).not.toHaveBeenCalled()
  expect(resyncRuntimeTranscript).not.toHaveBeenCalled()
  expect(deps.bumpProjectionVersion).toHaveBeenCalledTimes(5)
})

it('does not treat a replacement error code as replacement output', () => {
  const assistant = {
    id: 'assistant', role: 'assistant' as const, messages: [], timestamp: '', streaming: false,
  }
  expect(failureStage(assistant, true, false)).toBe('startup')
  expect(failureStage(assistant, true, true)).toBe('stream')
})

it('reads a WS failure code from the top level of the frame', () => {
  expect(wsFrameErrorCode({ code: ' agent.response_timeout ' })).toBe('agent.response_timeout')
  expect(wsFrameErrorCode({ code: '' })).toBe('')
  expect(wsFrameErrorCode({})).toBe('')
})

it('shows the local send-failed copy for an error frame that has no code', () => {
  const rejectAssistantStream = vi.fn()
  const assistantTurn = {
    id: 'assistant', role: 'assistant' as const, messages: [], timestamp: '', streaming: true,
  }
  const deps = {
    assistantStreams: {
      invocationIdForEvent: () => 'inv-1',
      getAssistantStream: () => ({ botId: 'bot', sessionId: 'session', assistantTurn }),
      rejectAssistantStream,
    },
    firstSend: { isResent: () => false },
    hasVisibleAssistantBlocks: () => true,
    removeTurnFromSession: vi.fn(),
    sendFailedMessage: () => 'local send failed',
  } as unknown as RuntimeIntegrationDeps
  createRuntimeIntegration(deps).handleWebSocketEvent(
    { type: 'error', message: 'upstream said: secret detail' } as never,
    'bot',
  )
  const failure = rejectAssistantStream.mock.calls[0]![1] as Error
  expect(failure.message).toBe('local send failed')
})

it('shows the local send-failed copy for a rejected run that has no code', () => {
  const rejectAssistantStream = vi.fn()
  const assistantTurn = {
    id: 'assistant', role: 'assistant' as const, messages: [], timestamp: '', streaming: true,
  }
  const deps = {
    assistantStreams: {
      getAssistantStream: () => ({ botId: 'bot', sessionId: 'session', assistantTurn }),
      rejectAssistantStream,
    },
    firstSend: { isResent: () => false },
    hasVisibleAssistantBlocks: () => true,
    removeTurnFromSession: vi.fn(),
    sendFailedMessage: () => 'local send failed',
  } as unknown as RuntimeIntegrationDeps
  createRuntimeIntegration(deps).handleWebSocketEvent(
    { type: 'run_rejected', invocation_id: 'inv-1', message: 'upstream said: secret detail' } as never,
    'bot',
  )
  expect((rejectAssistantStream.mock.calls[0]![1] as Error).message).toBe('local send failed')
})

/**
 * Failure Modes for WS `runtime_snapshot` reconnect reconciliation:
 * - FM-RI1: Initial `startSessionRuntime` hydration or cached session revisit (`!connection.prepared`)
 *           mistakenly sets `reconnected: true`, triggering redundant session-list & message refetches.
 * - FM-RI2: A mid-session WS drop + reconnect on an already prepared connection (`connection.prepared === true`)
 *           misses `onRuntimeSnapshotReconnected`, or double-refreshes messages when a settling run already
 *           invoked `refreshCurrentSession`.
 * - FM-RI3: Prepared WS reconnect arriving while a WS run is actively streaming (`status: 'running'`) or
 *           while a tentative first-send / streaming turn is live triggers a destructive REST history reload.
 */
describe('prepared WS runtime_snapshot reconnect reconciliation', () => {
  function buildWsPipeline() {
    const currentBotId = ref<string | null>('bot-1')
    const sessionId = ref<string | null>('session-1')
    let wsHandler: ((event: UIStreamEvent) => void) | null = null
    const socket: ChatWebSocket = {
      connected: true,
      send: vi.fn(),
      abort: vi.fn(),
      forget: vi.fn(),
      bindSession: vi.fn(),
      close: vi.fn(),
      onOpen: null,
      onClose: null,
    }

    const views = createChatViews({
      currentBotId,
      sessionId,
      rememberBackgroundTask: vi.fn(),
      applyPendingBackgroundEventsToTool: vi.fn(),
      bumpFsChangedAtIfFsMutation: vi.fn(),
    })

    const refreshSessionsList = vi.fn(async () => {})
    const refreshSessionMessages = vi.fn(async () => {})
    const refreshCurrentSession = vi.fn(async () => {})

    const sessionActivity = createSessionActivity({
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
    })

    let integration: ReturnType<typeof createRuntimeIntegration> | null = null
    const realtime = createChatRealtimeController(
      {
        onWebSocketEvent: (bid, event) => integration?.handleWebSocketEvent(event, bid),
        prepareSessionRuntime: async (_bid, _sid, commitInitialHistory) => {
          await commitInitialHistory(() => {})
        },
        onRuntimeProjection: (bid, sid, change) => integration?.handleProjection(bid, sid, change),
        onBotSessionsActivityEvent: vi.fn(),
      },
      {
        connectWebSocket: (_bid, handler) => {
          wsHandler = handler
          return socket
        },
        streamBotSessionsActivityEvents: vi.fn(async () => {}),
        createRetryingStream: () => ({ start: vi.fn(), stop: vi.fn() }),
      },
    )

    views.configure({
      runtimeProjection: sid => realtime.runtimeProjection(sid),
      startSessionRuntime: (bid, sid) => realtime.startSessionRuntime(bid, sid),
      stopSessionRuntime: (bid, sid) => realtime.stopSessionRuntime(bid, sid),
      discardDraft: vi.fn(),
      invalidateDraftCommand: vi.fn(),
      saveDraftExternalAgent: vi.fn(),
      activateDraftExternalAgent: vi.fn(),
      refreshAppliedHook: vi.fn(),
      ensureVisibleSummary: vi.fn(),
    })

    integration = createRuntimeIntegration({
      currentBotId,
      sessionId,
      explicitSessionSelection: ref(true),
      draftIntent: ref(false),
      focusedViewId: views.focusedViewId,
      firstSend: views.firstSend,
      workdirMismatchMessage: () => 'workdir mismatch',
      assistantStreams: views.assistantStreams,
      decisions: {
        observeRun: vi.fn(),
        handleControlAck: vi.fn(),
        cancelPendingForSession: vi.fn(),
      } as never,
      realtime,
      sessionList: {
        knownSessionSummary: () => null,
        upsertSession: vi.fn(),
      } as never,
      chatViews: views.chatViews,
      bumpProjectionVersion: () => {
        views.projectionVersion.value += 1
      },
      normalizeTarget: views.normalizeTarget,
      promoteDraftView: views.promoteDraftChatView,
      recordUserSent: views.recordUserSent,
      rescopeSessionCommandEventToComposer: vi.fn(),
      rememberCommandEvent: vi.fn(),
      removeTurnFromSession: vi.fn(),
      hasVisibleAssistantBlocks: () => false,
      finalizeStreamFailure: vi.fn(),
      refreshCurrentSession,
      resyncRuntimeTranscript: vi.fn(async () => {}),
      releaseHiddenSessionView: vi.fn(),
      loadInitialMessages: vi.fn(async () => {}),
      reattachTurnToSession: vi.fn(),
      sendFailedMessage: () => 'send failed',
      sendOutcomeUnknownMessage: () => 'unknown',
      touchSessionInList: vi.fn(),
      onRuntimeSnapshotReconnected: (bid, sid, options) =>
        sessionActivity.handleRuntimeSnapshotReconnected(bid, sid, options),
    })

    realtime.startWebSocket('bot-1')
    views.bindChatView('panel-chat', { botId: 'bot-1', sessionId: 'session-1', viewId: 'chat' }, true)

    return {
      socket,
      emitWs: (event: UIStreamEvent) => wsHandler!(event),
      views,
      realtime,
      sessionActivity,
      refreshSessionsList,
      refreshSessionMessages,
      refreshCurrentSession,
    }
  }

  async function flushMicrotasks() {
    await Promise.resolve()
    await Promise.resolve()
    await Promise.resolve()
  }

  it('does not treat initial hydration or cached revisit snapshots as reconnects, and reconciles idle messages once on prepared WS reconnect (FM-RI1, FM-RI2)', async () => {
    const {
      socket,
      emitWs,
      realtime,
      sessionActivity,
      refreshSessionsList,
      refreshSessionMessages,
    } = buildWsPipeline()

    // Initial hydration snapshot while `!connection.prepared`: must NOT trigger reconnect reconciliation.
    emitWs({
      type: 'runtime_snapshot',
      session_id: 'session-1',
      epoch: 'ep-1',
      seq: 1,
      snapshot: { bot_id: 'bot-1', session_id: 'session-1', epoch: 'ep-1', seq: 1, updated_at: '' },
    })
    await flushMicrotasks()
    expect(refreshSessionsList).not.toHaveBeenCalled()
    expect(refreshSessionMessages).not.toHaveBeenCalled()
    expect(sessionActivity.scheduleActivityRevisions.value).toEqual({})

    // Cached session revisit (`stopSessionRuntime` -> `startSessionRuntime` with retained epoch): still NOT a reconnect.
    realtime.stopSessionRuntime('bot-1', 'session-1')
    realtime.startSessionRuntime('bot-1', 'session-1')
    emitWs({
      type: 'runtime_snapshot',
      session_id: 'session-1',
      epoch: 'ep-1',
      seq: 2,
      snapshot: { bot_id: 'bot-1', session_id: 'session-1', epoch: 'ep-1', seq: 2, updated_at: '' },
    })
    await flushMicrotasks()
    expect(refreshSessionsList).not.toHaveBeenCalled()
    expect(refreshSessionMessages).not.toHaveBeenCalled()

    // Mid-session WS drop + reconnect while `connection.prepared === true`: reconciles schedule, list, and idle messages.
    socket.onClose?.()
    socket.onOpen?.()
    emitWs({
      type: 'runtime_snapshot',
      session_id: 'session-1',
      epoch: 'ep-2',
      seq: 1,
      snapshot: { bot_id: 'bot-1', session_id: 'session-1', epoch: 'ep-2', seq: 1, updated_at: '' },
    })
    await flushMicrotasks()
    expect(sessionActivity.scheduleActivityRevisions.value['bot-1']).toBe(1)
    expect(refreshSessionsList).toHaveBeenCalledTimes(1)
    expect(refreshSessionsList).toHaveBeenCalledWith('bot-1')
    expect(refreshSessionMessages).toHaveBeenCalledTimes(1)
    expect(refreshSessionMessages).toHaveBeenCalledWith('bot-1', 'session-1')
  })

  it('never triggers message history reload on prepared WS reconnect when an active WS run is streaming, and avoids duplicate reload when a run just settled (FM-RI2, FM-RI3)', async () => {
    const {
      socket,
      emitWs,
      refreshSessionsList,
      refreshSessionMessages,
      refreshCurrentSession,
    } = buildWsPipeline()

    // Complete initial hydration.
    emitWs({
      type: 'runtime_snapshot',
      session_id: 'session-1',
      epoch: 'ep-1',
      seq: 1,
      snapshot: { bot_id: 'bot-1', session_id: 'session-1', epoch: 'ep-1', seq: 1, updated_at: '' },
    })
    await flushMicrotasks()

    // Reconnect snapshot arrives with a live streaming run (`status: 'running'`):
    // session list & schedule reconcile, but NEITHER refreshCurrentSession NOR refreshSessionMessages may run!
    socket.onClose?.()
    socket.onOpen?.()
    emitWs({
      type: 'runtime_snapshot',
      session_id: 'session-1',
      epoch: 'ep-2',
      seq: 5,
      snapshot: {
        bot_id: 'bot-1',
        session_id: 'session-1',
        epoch: 'ep-2',
        seq: 5,
        updated_at: '',
        current_run_view: {
          run_id: 'run-active',
          turn_id: 'turn-active',
          generation: '1',
          status: 'running',
          messages: [],
          started_at: '',
          updated_at: '',
        },
      },
    })
    await flushMicrotasks()
    expect(refreshSessionsList).toHaveBeenCalledTimes(1)
    expect(refreshCurrentSession).not.toHaveBeenCalled()
    expect(refreshSessionMessages).not.toHaveBeenCalled()

    // Second reconnect where `run-active` settled while disconnected (`current_run` is now absent):
    // `handleProjection` calls `refreshCurrentSession` once for the finished run, and `alreadyRefreshedMessages: true`
    // prevents `refreshSessionMessages` from issuing a redundant second reload.
    socket.onClose?.()
    socket.onOpen?.()
    emitWs({
      type: 'runtime_snapshot',
      session_id: 'session-1',
      epoch: 'ep-2',
      seq: 9,
      snapshot: {
        bot_id: 'bot-1',
        session_id: 'session-1',
        epoch: 'ep-2',
        seq: 9,
        updated_at: '',
      },
    })
    await flushMicrotasks()
    expect(refreshSessionsList).toHaveBeenCalledTimes(2)
    expect(refreshCurrentSession).toHaveBeenCalledTimes(1)
    expect(refreshSessionMessages).not.toHaveBeenCalled()
  })
})

