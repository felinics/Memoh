import { createInvocationId } from '../chat-list.normalize'
import type { createAssistantStreamRegistry } from './assistant-streams'
import type { createChatDecisions } from './decisions'
import type { FirstSendTracker } from './first-send'
import type { createChatRealtimeController } from './realtime'
import { isRuntimeRunActive } from './runtime-projection'
import { awaitStopOutcome } from './stop-outcome'
import type { ChatAssistantTurn, ChatViewTarget } from './types'
import type { createChatViewRegistry } from './view-registry'

type AssistantStreams = ReturnType<typeof createAssistantStreamRegistry>
type Decisions = ReturnType<typeof createChatDecisions>
type Realtime = ReturnType<typeof createChatRealtimeController>
type ChatViews = ReturnType<typeof createChatViewRegistry>

export interface RuntimeAbortDeps {
  assistantStreams: AssistantStreams
  decisions: Decisions
  realtime: Realtime
  chatViews: ChatViews
  firstSend: FirstSendTracker
  normalizeTarget: (target?: Partial<ChatViewTarget>) => ChatViewTarget
  hasVisibleAssistantBlocks: (turn: ChatAssistantTurn) => boolean
}

// Owns deferred and active WebSocket run-abort state across session creation,
// run acceptance, user stop requests, and full user-scope teardown.
export function createRuntimeAbortController(deps: RuntimeAbortDeps) {
  const deferredAbortByInvocation = new Map<string, {
    runId: string
    botId: string
  }>()

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

  // A stop for a run that was accepted before its session was named waits
  // for session_created, which supplies the session the abort control needs.
  function replayDeferredAbort(invocationId: string, sessionId: string) {
    const deferredAbort = deferredAbortByInvocation.get(invocationId)
    if (!deferredAbort) return
    deferredAbortByInvocation.delete(invocationId)
    sendAbortControl(deferredAbort.runId, deferredAbort.botId, sessionId)
  }

  function recordAcceptedAbort(
    accepted: { invocationId: string; runId: string; botId: string; abortRequested: boolean } | undefined,
    sourceBotId: string,
    sessionId: string,
  ) {
    if (!accepted?.abortRequested) return
    const botId = accepted.botId || sourceBotId
    if (sessionId) {
      sendAbortControl(accepted.runId, botId, sessionId)
      return
    }
    deferredAbortByInvocation.set(accepted.invocationId, {
      runId: accepted.runId,
      botId,
    })
  }

  // Returns whether the stop reached the server or is queued to reach it once
  // the run or its session is named.
  function abortRun(invocationId: string): boolean {
    const runId = deps.assistantStreams.requestAbort(invocationId)
    if (!runId) return true
    const stream = deps.assistantStreams.getAssistantStream(invocationId)
    if (!stream?.sessionId.trim()) {
      deferredAbortByInvocation.set(invocationId, {
        runId,
        botId: stream?.botId ?? '',
      })
      return true
    }
    return sendAbortControl(
      runId,
      stream?.botId,
      stream?.sessionId,
    )
  }

  // awaitOutcome is the user's stop (see stop-outcome.ts). Every other stop
  // (deleting a session, switching bots) ends the send at once.
  function abort(target?: ChatViewTarget, options: { awaitOutcome?: boolean } = {}) {
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
      const stopSent = runtimeAborted || abortRun(invocationId)
      if (options.awaitOutcome && stopSent && awaitStopOutcome(deps, invocationId, abortError)) continue
      deps.assistantStreams.clearStopping(invocationId)
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
    replayDeferredAbort,
    recordAcceptedAbort,
    abortRun,
    abort,
    abortAllAssistantStreams,
  }
}
