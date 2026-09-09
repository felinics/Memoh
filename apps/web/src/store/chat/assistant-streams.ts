import { reactive, toRaw } from 'vue'
import type { ChatAssistantTurn } from './types'

export interface AssistantStream {
  readonly invocationId: string
  readonly runId: string
  readonly assistantTurn: ChatAssistantTurn
  readonly botId: string
  readonly sessionId: string
  readonly composerScope: string
  readonly viewId: string
}

// AcceptedRun is what run_accepted tells us: the server's name for a turn we
// submitted. It exists even for turns with no visible stream, such as a silent
// approval response, so a stop can still be addressed to them.
export interface AcceptedRun {
  readonly invocationId: string
  readonly runId: string
  readonly botId: string
  readonly sessionId: string
  readonly abortRequested: boolean
}

interface PendingAssistantStream extends AssistantStream {
  sessionId: string
  runId: string
  appendMessages: boolean
  messageIds: Map<number, number>
  onModelPreferenceSettled?: () => void
  resolve: () => void
  reject: (error: Error) => void
}

interface AssistantStreamMessage {
  id: number
  type: string
  tool_call_id?: string
}

export interface StreamIdentity {
  run_id?: string
  invocation_id?: string
  session_id?: string
}

export interface TrackAssistantStreamInput {
  onModelPreferenceSettled?: () => void
  invocationId: string
  assistantTurn: ChatAssistantTurn
  botId: string
  sessionId: string
  composerScope?: string
  viewId?: string
}

interface AssistantStreamRegistryDeps {
  finishAssistantTurn: (turn: ChatAssistantTurn) => void
}

type BeforeReject = (invocationId: string) => void

export function createAssistantStreamRegistry({ finishAssistantTurn }: AssistantStreamRegistryDeps) {
  // Keyed by invocation id, because a turn is registered before it is sent and
  // therefore before the server has named the run. The run id arrives later and
  // is indexed alongside so inbound events can be resolved by either name.
  const streams = reactive(new Map<string, PendingAssistantStream>())
  const invocationIdsByRunId = new Map<string, string>()
  const runIdsByInvocation = new Map<string, string>()
  // Stops pressed before the server named the run, replayed by bindRunId.
  const abortRequestedInvocations = new Map<string, {
    botId: string
    sessionId: string
  }>()
  const createdSessionsByInvocation = new Map<string, string>()

  function activeStreams(): PendingAssistantStream[] {
    return [...streams.values()]
  }

  function activeUnboundInvocationIds(botId: string | null | undefined, composerScope?: string): string[] {
    const bid = (botId ?? '').trim()
    const scope = composerScope?.trim()
    if (!bid) return []
    return activeStreams()
      .filter(stream => stream.botId === bid
        && !stream.sessionId
        && (!scope || stream.composerScope === scope))
      .map(stream => stream.invocationId)
  }

  function assistantStreamsForSession(
    botId: string | null | undefined,
    targetSessionId: string | null | undefined,
  ): AssistantStream[] {
    const bid = (botId ?? '').trim()
    const sid = (targetSessionId ?? '').trim()
    if (!bid || !sid) return []
    return activeStreams().filter(stream => stream.botId === bid && stream.sessionId === sid)
  }

  function isUnboundComposerStreaming(botId: string | null | undefined, composerScope?: string): boolean {
    return activeUnboundInvocationIds(botId, composerScope).length > 0
  }

  // Resolves an event to the local key for its turn. A run id is authoritative
  // once it exists; before that only the invocation names the turn.
  function invocationIdForEvent(event: StreamIdentity): string {
    const runId = (event.run_id ?? '').trim()
    if (runId) {
      const known = invocationIdsByRunId.get(runId)
      if (known) return known
    }
    const invocationId = (event.invocation_id ?? '').trim()
    if (invocationId) return invocationId
    // A run from another subscriber has no local invocation. Never attach it to
    // whichever local submission happens to be active in the same session.
    return runId
  }

  // Promise construction registers synchronously. Callers rely on the stream
  // being discoverable before ws.send() can synchronously replay an event.
  function trackAssistantStream(input: TrackAssistantStreamInput): Promise<void> {
    return new Promise<void>((resolve, reject) => {
      const id = input.invocationId.trim()
      if (!id) {
        reject(new Error('invocation_id is required'))
        return
      }
      if (streams.has(id)) {
        reject(new Error(`invocation_id ${id} is already active`))
        return
      }
      streams.set(id, {
        invocationId: id,
        runId: '',
        assistantTurn: input.assistantTurn,
        botId: input.botId,
        sessionId: input.sessionId.trim(),
        composerScope: input.composerScope?.trim() || 'chat',
        viewId: input.viewId?.trim() || 'chat',
        appendMessages: input.assistantTurn.messages.length > 0,
        messageIds: new Map(),
        onModelPreferenceSettled: input.onModelPreferenceSettled,
        resolve,
        reject,
      })
    })
  }

  // bindRunId records the server's name only while the local turn is active.
  // Epoch/seq projection ordering rejects late terminal frames, so no terminal
  // run-id history is needed here.
  function bindRunId(
    invocationId: string,
    runId: string,
    turnId: string,
  ): AcceptedRun | undefined {
    const invocation = invocationId.trim()
    const run = runId.trim()
    const turn = turnId.trim()
    if (!invocation || !run || !turn) return undefined
    const stream = streams.get(invocation)
    if (stream) {
      if (!stream.runId) stream.runId = run
      stream.assistantTurn.turnId = turn
      stream.assistantTurn.runtimeRunId = run
      invocationIdsByRunId.set(run, invocation)
      runIdsByInvocation.set(invocation, run)
    }
    const deferredAbort = abortRequestedInvocations.get(invocation)
    const abortRequested = abortRequestedInvocations.delete(invocation)
    return {
      invocationId: invocation,
      runId: run,
      botId: stream?.botId ?? deferredAbort?.botId ?? '',
      sessionId: stream?.sessionId ?? deferredAbort?.sessionId ?? '',
      abortRequested,
    }
  }

  // Only the matching live submission may release its ordering barrier.
  function settleModelPreference(event: StreamIdentity) {
    const stream = streams.get(event.invocation_id?.trim() || '')
    if (!stream?.runId || stream.runId !== event.run_id || stream.sessionId !== event.session_id) return
    const notify = stream.onModelPreferenceSettled
    stream.onModelPreferenceSettled = undefined
    notify?.()
  }

  // requestAbort resolves a stop to the run the server can address. Before
  // run_accepted no such name exists, so the intent is recorded and bindRunId
  // replays it. Silent turns without a visible stream are addressable too.
  function requestAbort(invocationId: string): string {
    const invocation = invocationId.trim()
    if (!invocation) return ''
    const runId = runIdsByInvocation.get(invocation) ?? ''
    if (runId) return runId
    const stream = streams.get(invocation)
    abortRequestedInvocations.set(invocation, {
      botId: stream?.botId ?? '',
      sessionId: stream?.sessionId ?? '',
    })
    return ''
  }

  function getAssistantStream(invocationId: string): AssistantStream | undefined {
    return streams.get(invocationId.trim())
  }

  // Each server-side continuation owns a fresh UI-message converter whose ids
  // start at zero. A response to ask_user / tool approval resumes inside the
  // existing assistant turn, so those run-local ids must be translated into
  // the turn's id namespace instead of overwriting its earlier blocks.
  function mapAssistantStreamMessage<T extends AssistantStreamMessage>(invocationId: string, message: T): T {
    const stream = streams.get(invocationId.trim())
    if (!stream) return message

    const mappedId = stream.messageIds.get(message.id)
    if (mappedId !== undefined) {
      return mappedId === message.id ? message : { ...message, id: mappedId }
    }

    const toolCallId = message.type === 'tool' ? message.tool_call_id?.trim() : ''
    const existingTool = toolCallId
      ? stream.assistantTurn.messages.find(block =>
          block.type === 'tool'
          && (block.toolCallId === toolCallId || block.tool_call_id === toolCallId),
        )
      : undefined

    let targetId = existingTool?.id
    if (targetId === undefined) {
      const turn = toRaw(stream.assistantTurn)
      const reservedIds = activeStreams()
        .filter(active => toRaw(active.assistantTurn) === turn)
        .flatMap(active => [...active.messageIds.values()])
      const occupiedIds = [...stream.assistantTurn.messages.map(block => block.id), ...reservedIds]
      targetId = stream.appendMessages || occupiedIds.includes(message.id)
        ? occupiedIds.reduce((maxId, id) => Math.max(maxId, id), -1) + 1
        : message.id
    }
    stream.messageIds.set(message.id, targetId)
    return targetId === message.id ? message : { ...message, id: targetId }
  }

  function finishAssistantStream(invocationId: string): PendingAssistantStream | undefined {
    const stream = streams.get(invocationId.trim())
    if (!stream) return undefined
    streams.delete(stream.invocationId)
    // Keep the accepted invocation/run correlation for the lifetime of this
    // user scope. A late frame must resolve to its own terminal invocation,
    // never to whichever run happens to be active now.
    if (!activeStreams().some(active => active.assistantTurn === stream.assistantTurn)) {
      finishAssistantTurn(stream.assistantTurn)
    }
    return stream
  }

  function resolveAssistantStream(invocationId: string) {
    finishAssistantStream(invocationId)?.resolve()
  }

  function rejectAssistantStream(invocationId: string, error: Error) {
    finishAssistantStream(invocationId)?.reject(error)
  }

  function discardAssistantStream(invocationId: string) {
    finishAssistantStream(invocationId)?.resolve()
  }

  function rejectAllStreams(error: Error, beforeReject?: BeforeReject) {
    for (const stream of activeStreams()) {
      beforeReject?.(stream.invocationId)
      rejectAssistantStream(stream.invocationId, error)
    }
  }

  // Deferred draft streams start unbound and may be assigned exactly once by
  // session_created. A duplicate or late event cannot move them to a new session.
  function recordCreatedSession(invocationId: string, targetSessionId: string): string {
    const id = invocationId.trim()
    const sid = targetSessionId.trim()
    if (!id || !sid) return ''
    const stream = streams.get(id)
    const canonicalSessionId = createdSessionsByInvocation.get(id) || stream?.sessionId || sid
    if (stream && !stream.sessionId) stream.sessionId = canonicalSessionId
    if (!createdSessionsByInvocation.has(id)) createdSessionsByInvocation.set(id, canonicalSessionId)
    return canonicalSessionId
  }

  function createdSessionIdForInvocation(invocationId: string): string {
    return createdSessionsByInvocation.get(invocationId.trim()) ?? ''
  }

  function forgetCreatedSession(invocationId: string) {
    createdSessionsByInvocation.delete(invocationId.trim())
  }

  function clearStreamHistory() {
    createdSessionsByInvocation.clear()
    invocationIdsByRunId.clear()
    runIdsByInvocation.clear()
    abortRequestedInvocations.clear()
  }

  return {
    activeUnboundInvocationIds,
    assistantStreamsForSession,
    isUnboundComposerStreaming,
    invocationIdForEvent,
    trackAssistantStream,
    bindRunId,
    settleModelPreference,
    requestAbort,
    getAssistantStream,
    mapAssistantStreamMessage,
    resolveAssistantStream,
    rejectAssistantStream,
    discardAssistantStream,
    rejectAllStreams,
    recordCreatedSession,
    createdSessionIdForInvocation,
    forgetCreatedSession,
    clearStreamHistory,
  }
}
