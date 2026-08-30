import type {
  RuntimeCurrentRunView,
  RuntimeDelta,
  RuntimeRunOperation,
  RuntimeSnapshot,
  UIMessage,
  UIRuntimeDeltaEvent,
  UIRuntimeSnapshotEvent,
  UITurn,
} from '@/composables/api/useChat.types'

export interface RuntimeTranscriptSlice {
  runId: string
  turnId: string
  // The originating send's client-issued id, echoed back by the server. Empty
  // for frames from before this field existed or from the rare pre-ledger run;
  // the transcript falls back to turnId-only matching for those.
  invocationId: string
  continuation?: boolean
  status: RuntimeCurrentRunView['status'] | null
  operation: RuntimeRunOperation | null
  turns: UITurn[]
  streaming: boolean
}

export interface RuntimeProjectionState {
  botId: string
  sessionId: string
  epoch: string
  seq: number
  currentRunView: RuntimeCurrentRunView | null
  transcript: RuntimeTranscriptSlice
}

export type RuntimeProjectionInput = UIRuntimeSnapshotEvent | UIRuntimeDeltaEvent

const activeRunStatuses = new Set<RuntimeCurrentRunView['status']>([
  'admitting',
  'running',
  'waiting_decision',
  'aborting',
])

export function isRuntimeRunActive(status?: string | null): boolean {
  return activeRunStatuses.has(status as RuntimeCurrentRunView['status'])
}

function cloneUIMessage(message: UIMessage): UIMessage {
  if (message.type === 'tool') {
    return {
      ...message,
      progress: message.progress ? [...message.progress] : undefined,
      approval: message.approval ? { ...message.approval } : undefined,
      execution_location: message.execution_location ? { ...message.execution_location } : undefined,
      user_input: message.user_input
        ? {
            ...message.user_input,
            questions: message.user_input.questions?.map(question => ({
              ...question,
              options: question.options?.map(option => ({ ...option })),
            })),
          }
        : undefined,
      background_task: message.background_task ? { ...message.background_task } : undefined,
    }
  }
  if (message.type === 'attachments') {
    return {
      ...message,
      attachments: message.attachments.map(attachment => ({ ...attachment })),
    }
  }
  return { ...message }
}

function cloneRunView(run: RuntimeCurrentRunView): RuntimeCurrentRunView {
  const messages = run.messages ?? []
  return {
    ...run,
    messages: messages.map(cloneUIMessage),
    user_turns: run.user_turns?.map(turn => ({
      ...turn,
      attachments: turn.attachments?.map(attachment => ({ ...attachment })),
      reply: turn.reply ? { ...turn.reply } : undefined,
      forward: turn.forward ? { ...turn.forward } : undefined,
    })),
    steer_turns: run.steer_turns?.map(turn => ({ ...turn })),
    request_user_turn: run.request_user_turn
      ? {
          ...run.request_user_turn,
          attachments: run.request_user_turn.attachments?.map(attachment => ({ ...attachment })),
          reply: run.request_user_turn.reply ? { ...run.request_user_turn.reply } : undefined,
          forward: run.request_user_turn.forward ? { ...run.request_user_turn.forward } : undefined,
        }
      : undefined,
    steer: run.steer ? { ...run.steer } : undefined,
    operation: run.operation
      ? {
          ...run.operation,
          replacement_user_turn: run.operation.replacement_user_turn
            ? { ...run.operation.replacement_user_turn }
            : undefined,
        }
      : undefined,
  }
}

function emptyTranscript(): RuntimeTranscriptSlice {
  return {
    runId: '',
    turnId: '',
    invocationId: '',
    continuation: false,
    status: null,
    operation: null,
    turns: [],
    streaming: false,
  }
}

function transcriptForRun(run: RuntimeCurrentRunView | null): RuntimeTranscriptSlice {
  if (!run) return emptyTranscript()
  const turnId = run.turn_id.trim()
  const turns: UITurn[] = []
  const fallbackUserTurn = run.request_user_turn ?? run.operation?.replacement_user_turn
  const userTurns = run.user_turns?.length
    ? run.user_turns
    : fallbackUserTurn
      ? [fallbackUserTurn]
      : []
  const active = isRuntimeRunActive(run.status)
  const steerTurns = [...(run.steer_turns ?? [])]
    .filter(steer => steer.status === 'applied' || active)
    .sort((left, right) => left.after_message_id - right.after_message_id
      || Date.parse(left.timestamp) - Date.parse(right.timestamp)
      || left.item_id.localeCompare(right.item_id))
  const steerDurableTurnIds = new Set(steerTurns.map(steer => steer.turn_id?.trim()).filter(Boolean))
  for (const userTurn of userTurns.filter(turn => !steerDurableTurnIds.has(turn.turn_id.trim()))) {
    const userTurnId = userTurn.turn_id.trim() || turnId
    turns.push({
      ...userTurn,
      turn_id: userTurnId,
      id: `runtime:${userTurnId}:user`,
    })
  }
  // A settled run with no streamed content has nothing to project. Emitting the
  // assistant shell here would let applyRuntimeTranscript's merge overwrite the
  // settled database turn's blocks with this empty list: once the run ends the
  // database history is authoritative, and idle snapshots arrive with
  // messages:null (e.g. after a backend restart, whose ledger view carries no
  // streamed blocks).
  const projectsAssistantContent = active
    || run.messages.length > 0
    || Boolean(run.error_code)
    || Boolean(run.error)
  if (projectsAssistantContent) {
    const assistantMessages = [...run.messages]
    if ((run.error_code || run.error) && !assistantMessages.some(message => message.type === 'error')) {
      assistantMessages.push({
        id: nextMessageId(assistantMessages),
        type: 'error',
        code: run.error_code,
        content: run.error ?? '',
      })
    }
    let segmentStart = 0
    let segmentTurnId = turnId
    let segmentTimestamp = run.started_at
    for (const steer of steerTurns) {
      let segmentEnd = segmentStart
      while (segmentEnd < assistantMessages.length
        && assistantMessages[segmentEnd]!.id <= steer.after_message_id) {
        segmentEnd += 1
      }
      const segment = assistantMessages.slice(segmentStart, segmentEnd)
      if (segment.length > 0) {
        turns.push(runtimeAssistantTurn(segmentTurnId, segmentTimestamp, segment))
      }
      const durable = steer.turn_id
        ? userTurns.find(turn => turn.turn_id.trim() === steer.turn_id?.trim())
        : undefined
      const steerTurnId = durable?.turn_id.trim() || `queue-steer:${steer.item_id}`
      turns.push({
        ...(durable ?? {
          turn_id: steerTurnId,
          role: 'user' as const,
          text: steer.text,
          timestamp: steer.timestamp,
        }),
        turn_id: steerTurnId,
        id: `runtime:queue-steer:${steer.item_id}:user`,
      })
      segmentStart = segmentEnd
      segmentTurnId = `queue-steer:${steer.item_id}:assistant`
      segmentTimestamp = steer.timestamp
    }
    // The final segment is the only live assistant after a steer boundary. It
    // intentionally exists while empty so the running indicator stays below
    // the newly admitted user input until the next model delta arrives.
    turns.push(runtimeAssistantTurn(segmentTurnId, segmentTimestamp, assistantMessages.slice(segmentStart)))
  }
  return {
    runId: run.run_id,
    turnId,
    invocationId: run.invocation_id?.trim() ?? '',
    continuation: Boolean(run.source_follow_up_item_id?.trim()),
    status: run.status,
    operation: run.operation ? { ...run.operation } : null,
    turns,
    streaming: isRuntimeRunActive(run.status),
  }
}

function runtimeAssistantTurn(turnId: string, timestamp: string, messages: UIMessage[]): UITurn {
  return {
    turn_id: turnId,
    role: 'assistant',
    id: `runtime:${turnId}:assistant`,
    timestamp,
    messages,
  }
}

function nextMessageId(messages: UIMessage[]): number {
  return messages.reduce((maximum, message) => Math.max(maximum, message.id), -1) + 1
}

function applyRunPatch(
  run: RuntimeCurrentRunView | null,
  delta: RuntimeDelta,
): RuntimeCurrentRunView | null {
  // Hot path (delta without a full view): shallow-copy the run and start from a
  // fresh messages array — the patch loops below copy-on-write the specific
  // messages they touch, so unchanged messages keep object identity and a text
  // append costs O(delta) instead of O(all content). The previous per-delta
  // cloneRunView made a stream of N deltas cost O(N x content), which is what
  // melted the main thread when a backgrounded tab replayed its backlog.
  // Full-view carriers still clone: that payload may be reused by the caller.
  let next = delta.current_run_view
    ? cloneRunView(delta.current_run_view)
    : run
      ? { ...run, messages: [...(run.messages ?? [])] }
      : null
  if (!next) return null
  const patch = delta.run
  if (patch && patch.run_id === next.run_id) {
    next = {
      ...next,
      ...(patch.status !== undefined ? { status: patch.status } : {}),
      ...(patch.error_code !== undefined ? { error_code: patch.error_code } : {}),
      ...(patch.error !== undefined ? { error: patch.error } : {}),
      ...(patch.steer !== undefined ? { steer: { ...patch.steer } } : {}),
      ...(patch.updated_at !== undefined ? { updated_at: patch.updated_at } : {}),
      ...(patch.owner_lease_expires_at !== undefined
        ? { owner_lease_expires_at: patch.owner_lease_expires_at }
        : {}),
    }
  }

  const messages = delta.reset_messages ? [] : next.messages
  const fallbackUserTurn = next.request_user_turn ?? next.operation?.replacement_user_turn
  const userTurns = next.user_turns?.length
    ? [...next.user_turns]
    : fallbackUserTurn
      ? [{ ...fallbackUserTurn }]
      : []
  const steerTurns = [...(next.steer_turns ?? [])]
  for (const incoming of delta.user_turn_upserts ?? []) {
    const turnId = incoming.turn_id.trim()
    if (!turnId) continue
    const cloned = {
      ...incoming,
      attachments: incoming.attachments?.map(attachment => ({ ...attachment })),
      reply: incoming.reply ? { ...incoming.reply } : undefined,
      forward: incoming.forward ? { ...incoming.forward } : undefined,
    }
    const index = userTurns.findIndex(turn => turn.turn_id.trim() === turnId)
    if (index < 0) userTurns.push(cloned)
    else userTurns[index] = cloned
  }
  for (const incoming of delta.steer_turn_upserts ?? []) {
    const itemId = incoming.item_id.trim()
    if (!itemId) continue
    const index = steerTurns.findIndex(turn => turn.item_id.trim() === itemId)
    if (index < 0) steerTurns.push({ ...incoming })
    else steerTurns[index] = { ...incoming }
  }
  const removedSteerItems = new Set((delta.steer_turn_removals ?? []).map(item => item.trim()).filter(Boolean))
  const retainedSteerTurns = removedSteerItems.size > 0
    ? steerTurns.filter(turn => !removedSteerItems.has(turn.item_id.trim()))
    : steerTurns
  for (const append of delta.message_appends ?? []) {
    const index = messages.findIndex(message => message.id === append.id && message.type === append.type)
    if (index < 0) {
      messages.push({ ...append })
      continue
    }
    const current = messages[index]
    if (current?.type !== 'text' && current?.type !== 'reasoning') continue
    messages[index] = { ...current, content: current.content + append.content }
  }
  for (const append of delta.progress_appends ?? []) {
    const index = messages.findIndex(message => message.id === append.id && message.type === 'tool')
    if (index < 0) continue
    const current = messages[index]
    if (current?.type !== 'tool') continue
    messages[index] = {
      ...current,
      ...(append.input !== undefined ? { input: append.input } : {}),
      progress: [...(current.progress ?? []), append.progress],
    }
  }
  for (const incoming of delta.message_upserts ?? []) {
    const cloned = cloneUIMessage(incoming)
    const toolCallId = cloned.type === 'tool' ? cloned.tool_call_id?.trim() : ''
    const index = messages.findIndex(message =>
      message.id === cloned.id
      || (
        toolCallId
        && message.type === 'tool'
        && message.tool_call_id?.trim() === toolCallId
      ),
    )
    if (index < 0) messages.push(cloned)
    else messages[index] = { ...cloned, id: messages[index]!.id }
  }
  messages.sort((left, right) => left.id - right.id)
  return { ...next, messages, user_turns: userTurns, steer_turns: retainedSteerTurns }
}

// Delta-only patch step, exported for the batching runtime client: accumulate
// many deltas onto a run view cheaply, then build the transcript once.
export function applyRuntimeRunPatch(
  run: RuntimeCurrentRunView | null,
  delta: RuntimeDelta,
): RuntimeCurrentRunView | null {
  return applyRunPatch(run, delta)
}

export function projectRuntimeTranscript(run: RuntimeCurrentRunView | null): RuntimeTranscriptSlice {
  return transcriptForRun(run)
}

export function createEmptyRuntimeProjection(sessionId = ''): RuntimeProjectionState {
  return {
    botId: '',
    sessionId,
    epoch: '',
    seq: 0,
    currentRunView: null,
    transcript: emptyTranscript(),
  }
}

export function reduceRuntimeProjection(
  state: RuntimeProjectionState,
  input: RuntimeProjectionInput,
): RuntimeProjectionState {
  if (input.type === 'runtime_snapshot') {
    const snapshot: RuntimeSnapshot = input.snapshot
    const currentRunView = snapshot.current_run_view
      ? cloneRunView(snapshot.current_run_view)
      : null
    return {
      botId: snapshot.bot_id,
      sessionId: input.session_id,
      epoch: input.epoch,
      seq: input.seq,
      currentRunView,
      transcript: transcriptForRun(currentRunView),
    }
  }

  const currentRunView = applyRunPatch(state.currentRunView, input.delta)
  return {
    ...state,
    sessionId: input.session_id,
    epoch: input.epoch,
    seq: input.seq,
    currentRunView,
    transcript: transcriptForRun(currentRunView),
  }
}
