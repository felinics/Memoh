import { ref, type Ref } from 'vue'
import type { UIStreamEvent } from '@/composables/api/useChat'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { isGuiToolName } from '@/utils/gui-tools'
import { createInvocationId } from '../chat-list.normalize'
import { provisionalSessionTitle } from '../chat-list.utils'
import type { createAssistantStreamRegistry } from './assistant-streams'
import type { createChatDecisions } from './decisions'
import type { createChatRealtimeController } from './realtime'
import type { RuntimeProjectionChange } from './runtime-client'
import { isRuntimeRunActive } from './runtime-projection'
import type { createSessionList } from './session-list'
import { CommandStreamError, StreamFailureError } from './send'
import type {
  ChatAssistantTurn,
  ChatMessage,
  ChatViewTarget,
  SendMessageStage,
} from './types'
import type { createChatViewRegistry } from './view-registry'

type AssistantStreams = ReturnType<typeof createAssistantStreamRegistry>
type Decisions = ReturnType<typeof createChatDecisions>
type Realtime = ReturnType<typeof createChatRealtimeController>
type SessionList = ReturnType<typeof createSessionList>
type ChatViews = ReturnType<typeof createChatViewRegistry>

interface GuiToolUseRequest {
  botId: string
  sessionId: string
  toolCallId: string
  toolName: string
  seq: number
}

export interface RuntimeIntegrationDeps {
  currentBotId: Ref<string | null>
  sessionId: Ref<string | null>
  focusedViewId: Ref<string>
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
  touchSessionInList: (sessionId: string, updatedAt?: string) => void
}

export function createRuntimeIntegration(deps: RuntimeIntegrationDeps) {
  const guiToolUseRequested = ref<GuiToolUseRequest | null>(null)
  const deferredAbortByInvocation = new Map<string, {
    runId: string
    botId: string
  }>()
  let guiRequestSequence = 0

  function handleSessionCreated(
    event: { invocation_id: string; session_id: string },
    sourceBotId = '',
  ) {
    const eventSessionId = event.session_id.trim()
    const pending = deps.assistantStreams.getAssistantStream(event.invocation_id)
    const botId = (
      pending?.botId
      || sourceBotId
      || deps.currentBotId.value
      || ''
    ).trim()
    if (!botId || !eventSessionId) return
    const originalSessionId = (pending?.sessionId ?? '').trim()
    const sessionId = deps.assistantStreams.recordCreatedSession(
      event.invocation_id,
      eventSessionId,
    ) || eventSessionId
    const deferredAbort = deferredAbortByInvocation.get(event.invocation_id)
    if (deferredAbort) {
      deferredAbortByInvocation.delete(event.invocation_id)
      sendAbortControl(deferredAbort.runId, deferredAbort.botId, sessionId)
    }
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
      const sessionId = event.session_id.trim()
      const botId = (
        accepted?.botId
        || sourceBotId
        || deps.currentBotId.value
        || ''
      ).trim()
      if (sessionId && botId) {
        deps.chatViews.getOrCreate({
          botId,
          sessionId,
          viewId: deps.focusedViewId.value,
        }).transcript.bindRuntimeTurn(
          event.invocation_id,
          turnId,
          event.run_id,
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
        event.message || deps.sendFailedMessage(),
      )
      const stage: SendMessageStage = deps.hasVisibleAssistantBlocks(
        rejected.assistantTurn,
      ) ? 'stream' : 'startup'
      if (rejected.assistantTurn.messages.length === 0) {
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
          new CommandStreamError(event.error?.message || 'slash command failed'),
        )
      }
      return
    }
    if (event.type === 'error') {
      const invocationId = deps.assistantStreams.invocationIdForEvent(event)
      const pending = deps.assistantStreams.getAssistantStream(invocationId)
      if (!pending) return
      const message = resolveApiErrorMessage(
        event,
        event.message || deps.sendFailedMessage(),
      )
      const stage: SendMessageStage = deps.hasVisibleAssistantBlocks(
        pending.assistantTurn,
      ) ? 'stream' : 'startup'
      if (pending.assistantTurn.messages.length === 0 && !event.code) {
        deps.removeTurnFromSession(
          pending.botId,
          pending.sessionId,
          pending.assistantTurn,
        )
      }
      deps.assistantStreams.rejectAssistantStream(
        invocationId,
        new StreamFailureError(message, stage, event.feedback ?? event),
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

    for (const message of currentRun.messages) {
      if (message.type !== 'tool' || !message.running || !isGuiToolName(message.name)) {
        continue
      }
      const previous = previousRun?.run_id === currentRun.run_id
        ? previousRun.messages.find(candidate =>
            candidate.type === 'tool'
            && (
              candidate.tool_call_id === message.tool_call_id
              || (!message.tool_call_id && candidate.id === message.id)
            ),
          )
        : undefined
      if (previous?.type === 'tool' && previous.running) continue
      guiToolUseRequested.value = {
        botId,
        sessionId,
        toolCallId: message.tool_call_id?.trim() ?? '',
        toolName: message.name,
        seq: ++guiRequestSequence,
      }
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
        const message = resolveApiErrorMessage(
          currentRun,
          currentRun.error || deps.sendFailedMessage(),
        )
        if (currentRun.status === 'aborted') {
          const aborted = new Error(message)
          aborted.name = 'AbortError'
          deps.assistantStreams.rejectAssistantStream(invocationId, aborted)
        } else {
          const stage: SendMessageStage = currentRun.messages.length > 0
            || Boolean(currentRun.error_code)
            ? 'stream'
            : 'startup'
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

  function abortAllAssistantStreams() {
    const abortError = new Error('aborted')
    abortError.name = 'AbortError'
    deps.assistantStreams.rejectAllStreams(abortError, abortRun)
    deferredAbortByInvocation.clear()
  }

  return {
    guiToolUseRequested,
    handleWebSocketEvent,
    handleProjection,
    prepareSessionRuntime,
    abort,
    abortAllAssistantStreams,
  }
}
