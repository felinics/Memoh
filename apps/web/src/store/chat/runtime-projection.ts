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
  const userTurn = run.request_user_turn ?? run.operation?.replacement_user_turn
  if (userTurn) {
    turns.push({
      ...userTurn,
      turn_id: turnId,
      id: `runtime:${turnId}:user`,
    })
  }
  // A settled run with no streamed content has nothing to project. Emitting the
  // assistant shell here would let applyRuntimeTranscript's merge overwrite the
  // settled database turn's blocks with this empty list: once the run ends the
  // database history is authoritative, and idle snapshots arrive with
  // messages:null (e.g. after a backend restart, whose ledger view carries no
  // streamed blocks).
  const projectsAssistantContent = isRuntimeRunActive(run.status)
    || run.messages.length > 0
    || Boolean(run.error_code)
    || Boolean(run.error)
  if (projectsAssistantContent) {
    turns.push({
      turn_id: turnId,
      role: 'assistant',
      id: `runtime:${turnId}:assistant`,
      timestamp: run.started_at,
      // Share message identity with the run view: consumers normalize into
      // their own view blocks without mutating UIMessage input, so re-cloning
      // every message per projection was pure O(all-content) waste.
      messages: [...run.messages],
    })
    if ((run.error_code || run.error) && !run.messages.some(message => message.type === 'error')) {
      turns[turns.length - 1] = {
        ...turns[turns.length - 1]!,
        role: 'assistant',
        messages: [
          ...run.messages,
          {
            id: nextMessageId(run.messages),
            type: 'error',
            code: run.error_code,
            content: run.error ?? '',
          },
        ],
      }
    }
  }
  return {
    runId: run.run_id,
    turnId,
    invocationId: run.invocation_id?.trim() ?? '',
    status: run.status,
    operation: run.operation ? { ...run.operation } : null,
    turns,
    streaming: isRuntimeRunActive(run.status),
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
  return { ...next, messages }
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
