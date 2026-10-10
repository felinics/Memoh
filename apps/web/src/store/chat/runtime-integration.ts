import type { Ref } from 'vue'
import type { UIStreamEvent } from '@/composables/api/useChat'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { createInvocationId, stringRecord } from '../chat-list.normalize'
import { provisionalSessionTitle } from '../chat-list.utils'
import type { createAssistantStreamRegistry } from './assistant-streams'
import type { createChatDecisions } from './decisions'
import type { createChatRealtimeController } from './realtime'
import type { RuntimeProjectionChange } from './runtime-client'
import { isRuntimeRunActive } from './runtime-projection'
import { fillRunErrorDetails } from './runtime-transcript-merge'
import type { createSessionList } from './session-list'
import { commandActionErrorMessage } from './messages'
import { CommandStreamError, StreamFailureError, failureStage } from './send'
import type {
  ChatAssistantTurn,
  ChatMessage,
  ChatViewTarget,
} from './types'
import type { createChatViewRegistry } from './view-registry'
import type { FirstSendTracker } from './first-send'

// The catalog code of a WS failure frame.
export function wsFrameErrorCode(event: { code?: string }): string {
  return event.code?.trim() ?? ''
}

type AssistantStreams = ReturnType<typeof createAssistantStreamRegistry>
type Decisions = ReturnType<typeof createChatDecisions>
type Realtime = ReturnType<typeof createChatRealtimeController>
type SessionList = ReturnType<typeof createSessionList>
type ChatViews = ReturnType<typeof createChatViewRegistry>

export interface RuntimeIntegrationDeps {
  currentBotId: Ref<string | null>
  sessionId: Ref<string | null>
  explicitSessionSelection: Ref<boolean>
  draftIntent: Ref<boolean>
  focusedViewId: Ref<string>
  firstSend: FirstSendTracker
  workdirMismatchMessage: () => string
  assistantStreams: AssistantStreams
  decisions: Decisions
  realtime: Realtime
  sessionList: SessionList
  chatViews: ChatViews
  bumpProjectionVersion: () => void
  normalizeTarget: (target?: Partial<ChatViewTarget>) => ChatViewTarget
  promoteDraftView: (target: ChatViewTarget, sessionId: string) => {
    transcript: { latestOptimisticUserText: () => string }
  }
  recordUserSent: (
    botId: string,
    sessionId: string,
    viewId: string,
    wasDraft: boolean,
  ) => void
  rescopeSessionCommandEventToComposer: (
    botId: string,
    sessionId: string,
    composerScope: string,
  ) => void
  rememberCommandEvent: (
    event: Extract<UIStreamEvent, { type: 'command_result' | 'command_error' }>,
    scope: { botId?: string; sessionId?: string; composerScope?: string },
  ) => void
  removeTurnFromSession: (
    botId: string,
    sessionId: string,
    turn: ChatMessage,
  ) => void
  hasVisibleAssistantBlocks: (turn: ChatAssistantTurn) => boolean
  finalizeStreamFailure: (
    turn: ChatAssistantTurn,
    botId: string,
    sessionId: string,
    error: Error,
  ) => void
  refreshCurrentSession: (botId: string, sessionId: string) => Promise<void>
  resyncRuntimeTranscript: (
    botId: string,
    sessionId: string,
    transcript: RuntimeProjectionChange['current']['transcript'],
  ) => Promise<void>
  releaseHiddenSessionView: (botId: string, sessionId: string) => void
  loadInitialMessages: (
    botId: string,
    sessionId: string,
    commitInitialHistory: (applyHistory: () => void) => Promise<void>,
  ) => Promise<void>
  reattachTurnToSession: (
    botId: string,
    sessionId: string,
    turn: ChatMessage,
  ) => void
  sendFailedMessage: () => string
  connectionLostMessage: () => string
  firstSendTimeoutMessage: () => string
  touchSessionInList: (sessionId: string, updatedAt?: string) => void
}

// How long a held first send waits for the server to confirm it. Creating the
// session and admitting the run normally take well under a second; without a
// limit a server that never answers leaves the pane locked on welcome.
const FIRST_SEND_CONFIRM_TIMEOUT_MS = 30_000

export function createRuntimeIntegration(deps: RuntimeIntegrationDeps) {
  const deferredAbortByInvocation = new Map<string, {
    runId: string
    botId: string
  }>()

  function handleSessionCreated(
    event: { invocation_id: string; session_id: string; workdir_id?: string },
    sourceBotId = '',
  ) {
    const eventSessionId = event.session_id.trim()
    if (!eventSessionId) return
    const pending = deps.assistantStreams.getAssistantStream(event.invocation_id)
    // A send that already ended locally (it failed, timed out or was stopped)
    // has given the draft back to the user. Its session must not take the
    // draft over now; only a stop still waiting for the session is sent.
    if (!pending) {
      replayDeferredAbort(event.invocation_id, eventSessionId)
      return
    }
    const botId = (
      pending.botId
      || sourceBotId
      || deps.currentBotId.value
      || ''
    ).trim()
    if (!botId) return
    const originalSessionId = pending.sessionId.trim()
    const sessionId = deps.assistantStreams.recordCreatedSession(
      event.invocation_id,
      eventSessionId,
    ) || eventSessionId
    // The workdir binding is fixed when the session is created, so a session
    // born without the requested workdir cannot be repaired afterwards. A
    // server that predates in-band workdir binding omits the field and
    // creates an unbound session; treat any mismatch as a startup failure.
    // The stop is replayed once run_accepted names the run, and send.ts
    // deletes the session; the draft never showed the send. The queued
    // resend is dropped so a reconnect cannot create the session again.
    const requestedWorkdirId = deps.firstSend.requestedWorkdirFor(event.invocation_id)
    if (requestedWorkdirId && (event.workdir_id ?? '').trim() !== requestedWorkdirId) {
      deps.realtime.forgetWebSocketRequest(botId, event.invocation_id)
      abortRun(event.invocation_id)
      deps.assistantStreams.rejectAssistantStream(
        event.invocation_id,
        new StreamFailureError(deps.workdirMismatchMessage(), 'startup', event),
      )
      return
    }
    const workdirId = (event.workdir_id ?? '').trim()
    deps.firstSend.admit(event.invocation_id, sessionId, workdirId)
    replayDeferredAbort(event.invocation_id, sessionId)
    // A held first send stays a draft until run_accepted reveals it; the
    // server can still refuse the run after creating the session.
    if (deps.firstSend.isAwaitingConfirmation(event.invocation_id)) {
      revealConfirmedFirstSend(event.invocation_id, botId)
      return
    }
    promoteCreatedSession(event.invocation_id, botId, sessionId, workdirId, originalSessionId)
  }

  // A stop for a run that was accepted before its session was named waits
  // for session_created, which supplies the session the abort control needs.
  function replayDeferredAbort(invocationId: string, sessionId: string) {
    const deferredAbort = deferredAbortByInvocation.get(invocationId)
    if (!deferredAbort) return
    deferredAbortByInvocation.delete(invocationId)
    sendAbortControl(deferredAbort.runId, deferredAbort.botId, sessionId)
  }

  // The server took a held first send once it has both named the session
  // and accepted the run (normally session_created, then run_accepted). Its
  // turns go on screen in the draft, then the draft becomes the session, in
  // one step.
  function revealConfirmedFirstSend(invocationId: string, sourceBotId: string) {
    const entry = deps.firstSend.entryForInvocation(invocationId)
    if (!entry || entry.revealed || !entry.sessionId || !entry.accepted) return
    const held = deps.assistantStreams.getAssistantStream(invocationId)
    const botId = (held?.botId || sourceBotId || deps.currentBotId.value || '').trim()
    if (!botId || !deps.firstSend.reveal(invocationId)) return
    // A first send leaves a draft, which has no session selected.
    promoteCreatedSession(invocationId, botId, entry.sessionId, entry.workdirId, '')
  }

  // Turns the draft the send came from into the session the server created:
  // the view moves, the sidebar row appears and the pane selects it.
  function promoteCreatedSession(
    invocationId: string,
    botId: string,
    sessionId: string,
    workdirId: string,
    originalSessionId: string,
  ) {
    const pending = deps.assistantStreams.getAssistantStream(invocationId)
    const viewId = pending?.viewId?.trim() || deps.focusedViewId.value
    const promoted = deps.promoteDraftView({
      botId,
      sessionId: null,
      viewId,
    }, sessionId)
    deps.realtime.startSessionRuntime(botId, sessionId)
    if ((deps.currentBotId.value ?? '').trim() !== botId) return

    const now = new Date().toISOString()
    if (!deps.sessionList.knownSessionSummary(sessionId)) {
      deps.sessionList.upsertSession({
        id: sessionId,
        bot_id: botId,
        type: 'chat',
        session_mode: 'chat',
        runtime_type: 'model',
        title: provisionalSessionTitle(promoted.transcript.latestOptimisticUserText()),
        // Places the row in its folder rather than Recents.
        workdir_id: workdirId || undefined,
        created_at: now,
        updated_at: now,
      })
    }
    deps.recordUserSent(botId, sessionId, viewId, true)
    if (
      deps.focusedViewId.value !== viewId
      || (deps.currentBotId.value ?? '').trim() !== botId
    ) return
    if ((deps.sessionId.value ?? '').trim() !== originalSessionId) return
    const composerScope = pending?.composerScope?.trim()
    if (composerScope && !originalSessionId) {
      deps.rescopeSessionCommandEventToComposer(botId, sessionId, composerScope)
    }
    deps.sessionId.value = sessionId
    // Same selection state a created session gets anywhere else: the user
    // chose it by sending, so a reload returns to it instead of the draft.
    deps.explicitSessionSelection.value = true
    deps.draftIntent.value = false
  }

  function handleWebSocketEvent(
    event: UIStreamEvent,
    sourceBotId = '',
  ) {
    if (event.type === 'control_ack') {
      deps.decisions.handleControlAck(event)
      return
    }
    if (event.type === 'session_created') {
      handleSessionCreated(event, sourceBotId)
      return
    }
    if (event.type === 'model_preference_settled') {
      deps.assistantStreams.settleModelPreference(event)
      return
    }
    if (event.type === 'run_accepted') {
      const turnId = event.turn_id.trim()
      if (!turnId) {
        const pending = deps.assistantStreams.getAssistantStream(event.invocation_id)
        if (pending) {
          deps.assistantStreams.rejectAssistantStream(
            event.invocation_id,
            new StreamFailureError(deps.sendFailedMessage(), 'startup', event),
          )
        }
        return
      }
      const accepted = deps.assistantStreams.bindRunId(
        event.invocation_id,
        event.run_id,
        turnId,
      )
      deps.firstSend.accept(event.invocation_id)
      revealConfirmedFirstSend(event.invocation_id, sourceBotId)
      const sessionId = event.session_id.trim()
      const botId = (
        accepted?.botId
        || sourceBotId
        || deps.currentBotId.value
        || ''
      ).trim()
      if (sessionId && botId) {
        // A send that already ended locally (a stop, or a failed first send
        // whose session was deleted) must not recreate a view for it.
        const view = deps.assistantStreams.getAssistantStream(event.invocation_id)
          ? deps.chatViews.getOrCreate({
              botId,
              sessionId,
              viewId: deps.focusedViewId.value,
            })
          : deps.chatViews.getSession(botId, sessionId)
        view?.transcript.bindRuntimeTurn(
          event.invocation_id,
          turnId,
          event.run_id,
          event.turn_position,
        )
      }
      if (accepted?.abortRequested) {
        const botId = accepted.botId || sourceBotId
        if (sessionId) sendAbortControl(accepted.runId, botId, sessionId)
        else {
          deferredAbortByInvocation.set(accepted.invocationId, {
            runId: accepted.runId,
            botId,
          })
        }
      }
      return
    }
    if (event.type === 'run_rejected') {
      const invocationId = event.invocation_id.trim()
      const rejected = deps.assistantStreams.getAssistantStream(invocationId)
      if (!rejected) return
      const message = resolveApiErrorMessage(
        event,
        deps.sendFailedMessage(),
      )
      const stage = failureStage(
        rejected.assistantTurn,
        rejected.replacesTurn,
        deps.hasVisibleAssistantBlocks(rejected.assistantTurn),
      )
      if (!deps.hasVisibleAssistantBlocks(rejected.assistantTurn)) {
        deps.removeTurnFromSession(
          rejected.botId,
          rejected.sessionId,
          rejected.assistantTurn,
        )
      }
      deps.assistantStreams.rejectAssistantStream(
        invocationId,
        new StreamFailureError(message, stage, event),
      )
      return
    }
    if (event.type === 'command_result' || event.type === 'command_error') {
      const invocationId = event.invocation_id?.trim() ?? ''
      const pending = invocationId
        ? deps.assistantStreams.getAssistantStream(invocationId)
        : undefined
      deps.rememberCommandEvent(event, {
        botId: pending?.botId || sourceBotId,
        sessionId: event.session_id || pending?.sessionId,
        composerScope: pending?.composerScope || event.composer_scope,
      })
      if (event.type === 'command_error' && invocationId && pending) {
        deps.assistantStreams.rejectAssistantStream(
          invocationId,
          new CommandStreamError(commandActionErrorMessage(event), event),
        )
      }
      return
    }
    if (event.type === 'error') {
      const invocationId = deps.assistantStreams.invocationIdForEvent(event)
      const pending = deps.assistantStreams.getAssistantStream(invocationId)
      const message = resolveApiErrorMessage(
        event,
        deps.sendFailedMessage(),
      )
      if (!pending) {
        const sessionId = event.session_id?.trim() ?? ''
        if (!sourceBotId || !sessionId) return
        const view = deps.chatViews.getSession(sourceBotId, sessionId)
        if (view) fillRunErrorDetails(view.transcript.messages, event.run_id ?? '', {
          code: wsFrameErrorCode(event) || undefined,
          content: message,
          args: stringRecord(event.args),
        })
        return
      }
      const stage = failureStage(
        pending.assistantTurn,
        pending.replacesTurn,
        deps.hasVisibleAssistantBlocks(pending.assistantTurn),
      )
      // History keeps a turn the server accepted and failed with a code; a
      // send refused before acceptance is not in history and returns to the
      // composer without leaving a turn behind.
      const acceptedWithCode = Boolean(pending.assistantTurn.runtimeRunId?.trim())
        && Boolean(wsFrameErrorCode(event))
      if (
        stage === 'startup'
        && !deps.hasVisibleAssistantBlocks(pending.assistantTurn)
        && !acceptedWithCode
      ) {
        deps.removeTurnFromSession(
          pending.botId,
          pending.sessionId,
          pending.assistantTurn,
        )
      }
      deps.assistantStreams.rejectAssistantStream(
        invocationId,
        new StreamFailureError(message, stage, event),
      )
    }
  }

  function handleProjection(
    botId: string,
    sessionId: string,
    change: RuntimeProjectionChange,
  ) {
    deps.bumpProjectionVersion()
    const view = deps.chatViews.getSession(botId, sessionId)
    const previousRun = change.previous.currentRunView
    const currentRun = change.current.currentRunView
    // Configuration saves have no chat output or history to reload. Applying
    // an empty transcript here would also disturb the previous settled reply.
    if (currentRun?.configuration_only || (!currentRun && previousRun?.configuration_only)) {
      deps.decisions.observeRun(sessionId, currentRun)
      return
    }
    const currentInvocationId = currentRun
      // The frame's own echo is authoritative; the run_id registry lookup only
      // covers frames from before the echo existed (pre-ledger runs).
      ? (currentRun.invocation_id?.trim() || deps.assistantStreams.invocationIdForEvent({
          run_id: currentRun.run_id,
          session_id: sessionId,
        }))
      : ''
    const currentPending = currentInvocationId
      ? deps.assistantStreams.getAssistantStream(currentInvocationId)
      : undefined
    const needsHistoryResync = Boolean(
      view && !view.transcript.applyRuntimeTranscript(change.current.transcript),
    )
    const resyncTranscript = () => deps.resyncRuntimeTranscript(
      botId,
      sessionId,
      change.current.transcript,
    )

    if (needsHistoryResync && currentRun && isRuntimeRunActive(currentRun.status)) {
      void resyncTranscript()
    }

    deps.decisions.observeRun(sessionId, currentRun)
    if (!currentRun) {
      if (previousRun) {
        const invocationId = deps.assistantStreams.invocationIdForEvent({
          run_id: previousRun.run_id,
          session_id: sessionId,
        })
        if (deps.assistantStreams.getAssistantStream(invocationId)) {
          deps.assistantStreams.resolveAssistantStream(invocationId)
        }
        if (view) {
          void deps.refreshCurrentSession(botId, sessionId)
            .finally(() => deps.releaseHiddenSessionView(botId, sessionId))
        } else {
          deps.touchSessionInList(sessionId, previousRun.updated_at)
        }
      }
      return
    }

    const wasActive = previousRun?.run_id === currentRun.run_id
      && isRuntimeRunActive(previousRun.status)
    const isActive = isRuntimeRunActive(currentRun.status)
    if (isActive || (previousRun?.run_id === currentRun.run_id && !wasActive)) return

    const invocationId = currentInvocationId
    const pending = currentPending
    if (pending) {
      if (currentRun.status === 'completed') {
        deps.assistantStreams.resolveAssistantStream(invocationId)
      } else {
        const message = resolveApiErrorMessage(currentRun, deps.sendFailedMessage())
        if (currentRun.status === 'aborted') {
          const aborted = new Error(message)
          aborted.name = 'AbortError'
          deps.assistantStreams.rejectAssistantStream(invocationId, aborted)
        } else {
          const hasReplacementOutput = currentRun.messages.some(message => message.type !== 'status')
          const stage = failureStage(
            pending.assistantTurn,
            pending.replacesTurn,
            pending.replacesTurn
              ? hasReplacementOutput
              : hasReplacementOutput || Boolean(currentRun.error_code),
          )
          deps.assistantStreams.rejectAssistantStream(
            invocationId,
            new StreamFailureError(message, stage, currentRun),
          )
        }
      }
      deps.releaseHiddenSessionView(botId, sessionId)
    }

    if (view && (needsHistoryResync || !pending)) {
      const refresh = needsHistoryResync
        ? resyncTranscript()
        : deps.refreshCurrentSession(botId, sessionId)
      void refresh
        .finally(() => deps.releaseHiddenSessionView(botId, sessionId))
    } else if (!view) {
      deps.touchSessionInList(sessionId, currentRun.updated_at)
    }
  }

  async function prepareSessionRuntime(
    botId: string,
    sessionId: string,
    commitInitialHistory: (applyHistory: () => void) => Promise<void>,
  ) {
    const normalizedBotId = botId.trim()
    const normalizedSessionId = sessionId.trim()
    if (!normalizedBotId || !normalizedSessionId) return
    try {
      await deps.loadInitialMessages(
        normalizedBotId,
        normalizedSessionId,
        commitInitialHistory,
      )
    } finally {
      for (const stream of deps.assistantStreams.assistantStreamsForSession(
        normalizedBotId,
        normalizedSessionId,
      )) {
        deps.reattachTurnToSession(
          normalizedBotId,
          normalizedSessionId,
          stream.assistantTurn,
        )
      }
    }
  }

  function abortRun(invocationId: string) {
    const runId = deps.assistantStreams.requestAbort(invocationId)
    if (!runId) return
    const stream = deps.assistantStreams.getAssistantStream(invocationId)
    if (!stream?.sessionId.trim()) {
      deferredAbortByInvocation.set(invocationId, {
        runId,
        botId: stream?.botId ?? '',
      })
      return
    }
    sendAbortControl(
      runId,
      stream?.botId,
      stream?.sessionId,
    )
  }

  function sendAbortControl(
    runId: string,
    botId?: string,
    sessionId?: string,
  ): boolean {
    const controlId = createInvocationId()
    const sid = sessionId?.trim() ?? ''
    const sent = deps.realtime.abortWebSocketRun(
      runId,
      botId,
      sid,
      controlId,
    )
    if (sent) deps.decisions.trackAbort(controlId, sid, runId)
    return sent
  }

  function abort(target?: ChatViewTarget) {
    const resolved = deps.normalizeTarget(target)
    const abortError = new Error('aborted')
    abortError.name = 'AbortError'
    const invocationIds = resolved.sessionId
      ? deps.assistantStreams.assistantStreamsForSession(
          resolved.botId,
          resolved.sessionId,
        ).map(stream => stream.invocationId)
      : deps.assistantStreams.activeUnboundInvocationIds(
          resolved.botId,
          target ? `${resolved.botId}:${resolved.viewId}` : undefined,
        )
    let runtimeAborted = false
    if (resolved.sessionId) {
      const runtime = deps.realtime.runtimeProjection(
        resolved.sessionId,
      )?.currentRunView
      if (runtime && isRuntimeRunActive(runtime.status)) {
        runtimeAborted = sendAbortControl(
          runtime.run_id,
          resolved.botId,
          resolved.sessionId,
        )
      }
    }
    for (const invocationId of invocationIds) {
      if (!runtimeAborted) abortRun(invocationId)
      deps.assistantStreams.rejectAssistantStream(invocationId, abortError)
    }
    deps.chatViews.prune()
  }

  // A socket that closes before a held first send is confirmed fails it: the
  // reliable request would otherwise wait for a reconnect indefinitely while
  // the pane shows a locked composer. Its queued resend is dropped, so a later
  // reconnect cannot start the run the user was told failed.
  function handleWebSocketClosed(botId: string) {
    const bid = botId.trim()
    for (const invocationId of deps.firstSend.awaitingConfirmationIds()) {
      const pending = deps.assistantStreams.getAssistantStream(invocationId)
      if (!pending || pending.botId.trim() !== bid) continue
      deps.realtime.forgetWebSocketRequest(bid, invocationId)
      deps.assistantStreams.rejectAssistantStream(
        invocationId,
        new StreamFailureError(deps.connectionLostMessage(), 'startup'),
      )
    }
  }

  // Fails a held first send the server has not confirmed in time, like a
  // dropped socket does. The stop is recorded too, so a run_accepted that
  // still arrives aborts the run instead of leaving it going unwatched.
  // Returns the cancel for the send to call once it settles.
  function watchFirstSendConfirmation(invocationId: string): () => void {
    const timer = setTimeout(() => {
      if (!deps.firstSend.isAwaitingConfirmation(invocationId)) return
      const pending = deps.assistantStreams.getAssistantStream(invocationId)
      if (!pending) return
      deps.realtime.forgetWebSocketRequest(pending.botId, invocationId)
      abortRun(invocationId)
      deps.assistantStreams.rejectAssistantStream(
        invocationId,
        new StreamFailureError(deps.firstSendTimeoutMessage(), 'startup'),
      )
    }, FIRST_SEND_CONFIRM_TIMEOUT_MS)
    return () => clearTimeout(timer)
  }

  function abortAllAssistantStreams() {
    const abortError = new Error('aborted')
    abortError.name = 'AbortError'
    deps.assistantStreams.rejectAllStreams(abortError, abortRun)
    deferredAbortByInvocation.clear()
  }

  return {
    handleWebSocketEvent,
    handleProjection,
    prepareSessionRuntime,
    handleWebSocketClosed,
    watchFirstSendConfirmation,
    abort,
    abortAllAssistantStreams,
  }
}
