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
import { RUNTIME_STEER_TURN_PREFIX } from './types'

export interface RuntimeTranscriptSlice {
  runId: string
  turnId: string
  // The sequence admission drew for turnId. Undefined only for frames from a
  // server that predates the field; those turns stay unnumbered and fall back
  // to the timestamp ordering, exactly as before.
  turnPosition?: number
  // The originating send's client-issued id, echoed back by the server. Empty
  // for frames from before this field existed or from the rare pre-ledger run;
  // the transcript falls back to turnId-only matching for those.
  invocationId: string
  continuation?: boolean
  // Turn ids in this frame that came from the queue's steer inputs. Callers
  // used to recognise a steer by its `queue-steer:` turn id; naming a steer at
  // claim time is precisely what removes that shape, so the frame names its
  // steers rather than leaving them to be read off an id. Optional like the
  // fields above: a frame without the key simply carries no steer.
  steerTurnIds?: string[]
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
  'finishing',
])

export function isRuntimeRunActive(status?: string | null): boolean {
  return activeRunStatuses.has(status as RuntimeCurrentRunView['status'])
}

// Configuration saves still own the session execution slot, but are not an
// assistant response. The server marks them from the first admitting frame.
export function isRuntimeRunStreaming(run?: RuntimeCurrentRunView | null): boolean {
  return !run?.configuration_only && isRuntimeRunActive(run?.status)
}

// Older snapshots carry a single request/replacement turn. Keep this wire
// adaptation shared by full projection and incremental delta application.
function userTurnsForRun(run: RuntimeCurrentRunView) {
  if (run.user_turns?.length) return run.user_turns
  const fallback = run.request_user_turn ?? run.operation?.replacement_user_turn
  return fallback ? [{ ...fallback }] : []
}

// A run owns several turns, not one: an applied steer opens its own canonical
// turn inside the same run, and the output after it is filed there
// (SR-TURN-001, docs/design/session-input-queues.md). Comparing against
// run.turn_id alone declares every post-steer turn foreign to the run that is
// still producing it, which is what let a mid-run history refresh drop live
// steer output off the screen.
export function runOwnsTurn(
  run: RuntimeCurrentRunView | null | undefined,
  turnId: string,
): boolean {
  const target = turnId.trim()
  if (!run || !target) return false
  if (run.turn_id.trim() === target) return true
  // userTurnsForRun owns the whole wire-shape chain — user_turns, the legacy
  // request_user_turn, and an edit's replacement turn. Re-deriving it here
  // duplicated two thirds of it and dropped the third, so a retry or edit whose
  // replacement was the run's only user turn read as foreign to its own run.
  if (userTurnsForRun(run).some(turn => turn.turn_id.trim() === target)) return true
  return (run.steer_turns ?? []).some((steer) => {
    if (steer.turn_id?.trim() === target) return true
    // A steer that has not committed yet has no durable turn, so both its
    // bubble and the segment under it carry an identity minted from this run's
    // own queue item. The item is therefore the match.
    return target === provisionalSteerTurnId(steer.item_id)
  })
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
    turnPosition: undefined,
    invocationId: '',
    continuation: false,
    steerTurnIds: [],
    status: null,
    operation: null,
    turns: [],
    streaming: false,
  }
}

function transcriptForRun(run: RuntimeCurrentRunView | null): RuntimeTranscriptSlice {
  if (!run || run.configuration_only) return emptyTranscript()
  const turnId = run.turn_id.trim()
  const turns: UITurn[] = []
  const userTurns = userTurnsForRun(run)
  // A run spans several turns once a steer opens one (SR-TURN-001), so a
  // segment is numbered by the turn it is filed under, not by the run. The
  // run's own position covers its request turn; a steer's turn arrives
  // numbered from history, and stays unnumbered until that commit lands.
  const positionOf = (segmentTurnId: string): number | undefined => {
    const id = segmentTurnId.trim()
    if (!id) return undefined
    const owner = userTurns.find(turn => turn.turn_id.trim() === id)
    if (owner?.turn_position !== undefined) return owner.turn_position
    if (id === turnId) return run.turn_position
    // A claimed steer knows its slot before its row exists.
    return (run.steer_turns ?? []).find(steer => steer.turn_id?.trim() === id)?.turn_position
  }
  const active = isRuntimeRunActive(run.status)
  const steerTurnIds: string[] = []
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
      turn_position: userTurn.turn_position ?? positionOf(userTurnId),
      id: `runtime:${userTurnId}:user`,
    })
  }
  // A settled run with no streamed content has nothing to project. Emitting the
  // assistant shell here would let applyRuntimeTranscript's merge overwrite the
  // settled database turn's blocks with this empty list: once the run ends the
  // database history is authoritative, and idle snapshots arrive with
  // messages:null (e.g. after a backend restart, whose ledger view carries no
  // streamed blocks).
  // Runtime activity belongs to the current run, even when a steer splits its
  // transcript. Keep it out of the historical segments defined by message IDs.
  const runtimeStatus = active ? run.messages.find(message => message.type === 'status') : undefined
  const assistantMessages = run.messages.filter(message => message.type !== 'status')
  const projectsAssistantContent = active
    || assistantMessages.length > 0
    || Boolean(run.error_code)
  if (projectsAssistantContent) {
    // The block is rendered from the code; the run carries no error text.
    if (run.error_code && !assistantMessages.some(message => message.type === 'error')) {
      assistantMessages.push({
        id: nextMessageId(run.messages),
        type: 'error',
        code: run.error_code,
        content: '',
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
        turns.push(runtimeAssistantTurn(segmentTurnId, segmentTimestamp, segment, positionOf(segmentTurnId)))
      }
      // The server names a steer's turn when it claims the input, so the name
      // is authoritative from the first frame — waiting for the persisted user
      // turn to appear in user_turns left history and the projection disagreeing
      // about one input for as long as the commit took to publish. The
      // provisional identity only covers a server that does not send one.
      const durableTurnId = steer.turn_id?.trim() ?? ''
      const durable = durableTurnId
        ? userTurns.find(turn => turn.turn_id.trim() === durableTurnId)
        : undefined
      const steerTurnId = durableTurnId || provisionalSteerTurnId(steer.item_id)
      const steerTurnPosition = durable?.turn_position ?? steer.turn_position
      steerTurnIds.push(steerTurnId)
      turns.push({
        ...(durable ?? {
          turn_id: steerTurnId,
          role: 'user' as const,
          text: steer.text,
          timestamp: steer.timestamp,
        }),
        turn_id: steerTurnId,
        turn_position: steerTurnPosition,
        id: `runtime:${RUNTIME_STEER_TURN_PREFIX}${steer.item_id}:user`,
      })
      segmentStart = segmentEnd
      // History persists an applied steer as its own turn and files the
      // assistant output that follows it under that turn. Name the live
      // segment after the durable turn as soon as it is known, so the settled
      // page replaces this segment instead of rendering beside it. Until then
      // both halves carry the same provisional identity: role already
      // separates them (turnIdentityKey is turn id + role, render ids end in
      // :user / :assistant), while a distinct id for the segment left the two
      // with nothing in common for the sort to key on, and the reply rendered
      // above the steer that asked for it.
      segmentTurnId = steerTurnId
      segmentTimestamp = steer.timestamp
    }
    // The final segment is the only live assistant after a steer boundary. It
    // intentionally exists while empty so the running indicator stays below
    // the newly admitted user input until the next model delta arrives. Once
    // the run has settled an empty segment would only render a blank turn.
    const finalSegment: UIMessage[] = assistantMessages.slice(segmentStart)
    if (runtimeStatus) finalSegment.push(runtimeStatus)
    if (active || finalSegment.length > 0 || turns.every(turn => turn.role !== 'assistant')) {
      turns.push(runtimeAssistantTurn(segmentTurnId, segmentTimestamp, finalSegment, positionOf(segmentTurnId)))
    }
  }
  return {
    runId: run.run_id,
    turnId,
    turnPosition: run.turn_position,
    invocationId: run.invocation_id?.trim() ?? '',
    continuation: false,
    steerTurnIds,
    status: run.status,
    operation: run.operation ? { ...run.operation } : null,
    turns,
    streaming: isRuntimeRunActive(run.status),
  }
}

// The identity a steer's turns carry until its step commit mints the durable
// one. Minted from the queue item, so it is stable across frames.
function provisionalSteerTurnId(itemID: string): string {
  return `${RUNTIME_STEER_TURN_PREFIX}${itemID.trim()}`
}

function runtimeAssistantTurn(
  turnId: string,
  timestamp: string,
  messages: UIMessage[],
  turnPosition?: number,
): UITurn {
  return {
    turn_id: turnId,
    turn_position: turnPosition,
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
      ...(patch.updated_at !== undefined ? { updated_at: patch.updated_at } : {}),
      ...(patch.owner_lease_expires_at !== undefined
        ? { owner_lease_expires_at: patch.owner_lease_expires_at }
        : {}),
    }
    if (patch.clear_retry) {
      const { retry: _cleared, ...rest } = next
      next = rest
    } else if (patch.retry !== undefined) {
      next = { ...next, retry: { ...patch.retry } }
    }
  }

  const messages = delta.reset_messages ? [] : next.messages
  const userTurns = [...userTurnsForRun(next)]
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
