import { computed, reactive, toRaw, type Ref } from 'vue'
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
  readonly abortRequested: boolean
}

interface PendingAssistantStream extends AssistantStream {
  sessionId: string
  runId: string
  appendMessages: boolean
  messageIds: Map<number, number>
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
  invocationId: string
  assistantTurn: ChatAssistantTurn
  botId: string
  sessionId: string
  composerScope?: string
  viewId?: string
}

interface AssistantStreamRegistryDeps {
  currentBotId: Ref<string | null>
  sessionId: Ref<string | null>
  finishAssistantTurn: (turn: ChatAssistantTurn) => void
}

type BeforeReject = (invocationId: string) => void

const TERMINAL_STREAM_HISTORY_LIMIT = 512
const RUN_ID_HISTORY_LIMIT = 512

export function createAssistantStreamRegistry({ currentBotId, sessionId, finishAssistantTurn }: AssistantStreamRegistryDeps) {
  // Keyed by invocation id, because a turn is registered before it is sent and
  // therefore before the server has named the run. The run id arrives later and
  // is indexed alongside so inbound events can be resolved by either name.
  const streams = reactive(new Map<string, PendingAssistantStream>())
  const invocationIdsByRunId = new Map<string, string>()
  const runIdsByInvocation = new Map<string, string>()
  // Stops pressed before the server named the run, replayed by bindRunId.
  const abortRequestedInvocations = new Set<string>()
  const createdSessionsByInvocation = new Map<string, string>()
  const terminalInvocationIds = new Set<string>()

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

  function isSessionStreaming(
    botId: string | null | undefined,
    targetSessionId: string | null | undefined,
  ): boolean {
    return assistantStreamsForSession(botId, targetSessionId).length > 0
  }

  function isUnboundComposerStreaming(botId: string | null | undefined, composerScope?: string): boolean {
    return activeUnboundInvocationIds(botId, composerScope).length > 0
  }

  const streamingSessionId = computed(() => {
    const bid = (currentBotId.value ?? '').trim()
    const activeSid = (sessionId.value ?? '').trim()
    const activeSessionIds = activeStreams()
      .filter(stream => stream.botId === bid)
      .map(stream => stream.sessionId)
      .filter(Boolean)
    if (activeSid && activeSessionIds.includes(activeSid)) return activeSid
    return activeSessionIds[0] ?? null
  })

  const streaming = computed(() => {
    const bid = (currentBotId.value ?? '').trim()
    const activeSid = (sessionId.value ?? '').trim()
    return activeSid
      ? isSessionStreaming(bid, activeSid)
      : isUnboundComposerStreaming(bid)
  })

  function fallbackInvocationId(botId: string, targetSessionId?: string | null): string {
    const bid = botId.trim() || 'unbound'
    const sid = (targetSessionId ?? '').trim()
    return sid ? `session:${bid}:${sid}:agent-run` : `bot:${bid}:orphan-run`
  }

  // Resolves an event to the local key for its turn. A run id is authoritative
  // once it exists; before that only the invocation names the turn.
  function invocationIdForEvent(botId: string, event: StreamIdentity, targetSessionId?: string): string {
    const runId = (event.run_id ?? '').trim()
    if (runId) {
      const known = invocationIdsByRunId.get(runId)
      if (known) return known
    }
    const invocationId = (event.invocation_id ?? '').trim()
    if (invocationId) return invocationId
    // A run we never submitted (another tab, or a reconnect) has no local
    // invocation, so fall back to the session's single active turn.
    const sid = (event.session_id ?? targetSessionId ?? '').trim()
    const activeIds = assistantStreamsForSession(botId, sid).map(stream => stream.invocationId)
    if (activeIds.length === 1) return activeIds[0]!
    return runId || fallbackInvocationId(botId, sid)
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
      if (terminalInvocationIds.has(id)) {
        reject(new Error(`invocation_id ${id} is already terminal`))
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
        resolve,
        reject,
      })
    })
  }

  // bindRunId records the server's name for a turn we submitted. The mapping is
  // kept even when no local stream is tracking that turn — a silent approval
  // response, for instance — so its events are never re-pointed at an unrelated
  // turn by the session fallback above, and a deferred stop can still reach it.
  function bindRunId(invocationId: string | undefined, runId: string | undefined): AcceptedRun | undefined {
    const invocation = invocationId?.trim()
    const run = runId?.trim()
    if (!invocation || !run) return undefined
    rememberRunId(run, invocation)
    const stream = streams.get(invocation)
    if (stream && !stream.runId) stream.runId = run
    const abortRequested = abortRequestedInvocations.delete(invocation)
    return { invocationId: invocation, runId: run, botId: stream?.botId ?? '', abortRequested }
  }

  function rememberRunId(runId: string, invocationId: string) {
    invocationIdsByRunId.set(runId, invocationId)
    runIdsByInvocation.set(invocationId, runId)
    if (invocationIdsByRunId.size <= RUN_ID_HISTORY_LIMIT) return
    const oldestRun = invocationIdsByRunId.keys().next().value
    if (!oldestRun) return
    const oldestInvocation = invocationIdsByRunId.get(oldestRun)
    invocationIdsByRunId.delete(oldestRun)
    if (oldestInvocation && runIdsByInvocation.get(oldestInvocation) === oldestRun) {
      runIdsByInvocation.delete(oldestInvocation)
    }
  }

  // requestAbort resolves a stop to the run the server can address. Before
  // run_accepted no such name exists, so the intent is recorded and bindRunId
  // replays it. Silent turns without a visible stream are addressable too.
  function requestAbort(invocationId: string): string {
    const invocation = invocationId.trim()
    if (!invocation) return ''
    const runId = runIdsByInvocation.get(invocation) ?? ''
    if (runId) return runId
    abortRequestedInvocations.add(invocation)
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
    rememberTerminalInvocation(stream.invocationId)
    streams.delete(stream.invocationId)
    // The run keeps its mapping past terminal so late events for it resolve to
    // a known-terminal invocation instead of resurrecting another turn.
    if (!activeStreams().some(active => active.assistantTurn === stream.assistantTurn)) {
      finishAssistantTurn(stream.assistantTurn)
    }
    return stream
  }

  function rememberTerminalInvocation(invocationId: string) {
    const id = invocationId.trim()
    if (!id) return
    terminalInvocationIds.add(id)
    if (terminalInvocationIds.size <= TERMINAL_STREAM_HISTORY_LIMIT) return
    const oldest = terminalInvocationIds.values().next().value
    if (oldest) terminalInvocationIds.delete(oldest)
  }

  function isTerminalInvocation(invocationId: string | undefined): boolean {
    const id = invocationId?.trim()
    return Boolean(id && terminalInvocationIds.has(id))
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
  function recordCreatedSession(invocationId: string | undefined, targetSessionId: string): string {
    const id = invocationId?.trim()
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
    terminalInvocationIds.clear()
    invocationIdsByRunId.clear()
    runIdsByInvocation.clear()
    abortRequestedInvocations.clear()
  }

  return {
    streaming,
    streamingSessionId,
    activeUnboundInvocationIds,
    assistantStreamsForSession,
    isSessionStreaming,
    isUnboundComposerStreaming,
    invocationIdForEvent,
    trackAssistantStream,
    bindRunId,
    requestAbort,
    getAssistantStream,
    mapAssistantStreamMessage,
    resolveAssistantStream,
    rejectAssistantStream,
    discardAssistantStream,
    isTerminalInvocation,
    rejectAllStreams,
    recordCreatedSession,
    createdSessionIdForInvocation,
    forgetCreatedSession,
    clearStreamHistory,
  }
}
