import { isRuntimeSteerTurnId, type ChatAssistantTurn, type ChatMessage, type ChatUserTurn, type ContentBlock, type ErrorBlock } from './types'
import { isRuntimeRunActive, type RuntimeTranscriptSlice } from './runtime-projection'

type RuntimeChatTurn = ChatUserTurn | ChatAssistantTurn

// Keep unrelated history in server order while placing each runtime turn in
// its own slot. One run can straddle channel turns after a steer; reinserting
// the whole run at its first turn moves those channel turns behind its output.
export function insertRuntimeTurns(
  messages: ChatMessage[],
  turns: RuntimeChatTurn[],
  fallbackIndex: number,
): void {
  let cursor = 0
  for (const turn of turns) {
    let index: number
    if (turn.turnPosition !== undefined) {
      const position = turn.turnPosition
      // A turn with no position sorts after every numbered turn: positions come
      // from one monotonic per-session counter, so whatever numbers it later
      // will number it past this one. Treating it as an opaque skip instead let
      // a numbered turn land behind an unnumbered tail — the run's streaming
      // reply rendered below a local turn the server had not named yet — and
      // disagreed with sortChatMessages, which already orders them this way.
      index = messages.findIndex((other, i) =>
        i >= cursor && (other.turnPosition === undefined || other.turnPosition > position),
      )
      if (index < 0) index = messages.length
    } else if (isRuntimeSteerTurnId(turn.turnId)) {
      // A provisional steer has not reserved a durable position yet.
      index = messages.length
    } else {
      // Older servers omit positions. Preserve their existing insertion anchor
      // and the frame's order instead of inferring placement from timestamps.
      index = Math.max(cursor, fallbackIndex)
    }
    messages.splice(index, 0, turn)
    cursor = index + 1
  }
}

export function markRuntimeTurn(
  turn: RuntimeChatTurn,
  slice: RuntimeTranscriptSlice,
  originalUser: boolean,
): RuntimeChatTurn {
  // Read before the refile below can rewrite turnId onto the run's request
  // turn: what makes this a steer is the frame listing it, not its id.
  const steered = Boolean(turn.turnId) && (slice.steerTurnIds ?? []).includes(turn.turnId!)
  // An assistant segment keeps its own turn identity when it is provisional
  // (steer prefix) or nested under a user turn the same frame carries: that
  // is the durable turn history files the post-steer output under. Any other
  // assistant turn belongs to the run's request turn.
  const nestedAssistantSegment = turn.role === 'assistant' && Boolean(turn.turnId) && (
    isRuntimeSteerTurnId(turn.turnId)
    || slice.turns.some(other => other.role === 'user' && other.turn_id.trim() === turn.turnId)
  )
  if (originalUser || !turn.turnId || (turn.role === 'assistant' && !nestedAssistantSegment)) {
    turn.turnId = slice.turnId
    // The turn was just refiled under the run's request turn, so the run's slot
    // is the right fallback for a segment that has no number of its own. A turn
    // that arrived numbered keeps that number: the frame carries the database
    // row's position, which outranks anything derived here.
    turn.turnPosition ??= slice.turnPosition
  }
  turn.runtimeRunId = slice.runId
  if (turn.role === 'user' && steered) turn.runtimeSteer = true
  if (turn.role === 'user' && slice.continuation) turn.runtimeContinuation = true
  turn.__optimistic = false
  if (turn.role === 'assistant') turn.streaming = slice.streaming
  return turn
}

// The run view names a failure by its code alone; the args and copy of the
// same failure arrive only on the error frame. Fills them into block from
// source when both name the same code, so neither arrival order loses them.
// Copy rendered without args is replaced together with the missing args.
function fillErrorBlockDetails(block: ErrorBlock, source: Pick<ErrorBlock, 'code' | 'content' | 'args'>): void {
  if (!block.code || block.code !== source.code) return
  if (!block.args && source.args) {
    block.args = source.args
    if (source.content) block.content = source.content
    return
  }
  if (!block.content) block.content = source.content
}

// Carries the error details current already holds onto next, the same turn
// arriving again from a run view or a history page.
export function inheritErrorDetails(next: ChatAssistantTurn, current: ChatAssistantTurn): void {
  const known = current.messages.filter(isErrorBlock)
  if (known.length === 0) return
  for (const block of next.messages.filter(isErrorBlock)) {
    const source = known.find(other => other.code === block.code)
    if (source) fillErrorBlockDetails(block, source)
  }
}

// Fills every error block of turn from source; reports whether turn has one.
export function fillTurnErrorDetails(turn: ChatAssistantTurn, source: Pick<ErrorBlock, 'code' | 'content' | 'args'>): boolean {
  const blocks = turn.messages.filter(isErrorBlock)
  for (const block of blocks) fillErrorBlockDetails(block, source)
  return blocks.length > 0
}

// An error frame that arrives after its run already settled the stream
// still carries the args and copy the run view lacks.
export function fillRunErrorDetails(messages: ChatMessage[], runId: string, source: Pick<ErrorBlock, 'code' | 'content' | 'args'>): void {
  const run = runId.trim()
  if (!run) return
  const turn = messages.find((message): message is ChatAssistantTurn =>
    message.role === 'assistant' && message.runtimeRunId === run)
  if (turn) fillTurnErrorDetails(turn, source)
}

function isErrorBlock(block: ContentBlock): block is ErrorBlock {
  return block.type === 'error'
}

// Reconciles one authoritative runtime frame without changing render
// identities already owned by optimistic or settled turns.
export function reconcileRuntimeTurns(
  existing: RuntimeChatTurn[],
  incoming: RuntimeChatTurn[],
): RuntimeChatTurn[] {
  const used = new Set<RuntimeChatTurn>()
  const resolved = incoming.map((next) => {
    const current = existing.find(turn =>
      !used.has(turn)
      && turn.role === next.role
      && turn.turnId === next.turnId,
    )
    if (!current) return next
    used.add(current)
    if (current.role === 'assistant' && next.role === 'assistant') inheritErrorDetails(next, current)
    const renderId = current.id
    const settledPosition = current.turnPosition ?? next.turnPosition
    Object.assign(current, next, { id: renderId, turnPosition: settledPosition })
    return current
  })
  if (!incoming.some(turn => turn.role === 'assistant')) {
    const assistant = existing.find(turn => turn.role === 'assistant')
    if (assistant && !used.has(assistant)) resolved.push(assistant)
  }
  if (!incoming.some(turn => turn.role === 'user')) {
    // Admission and the first streamed frame can arrive separately. The
    // latter often carries only the assistant shell; do not remove the
    // already-rendered optimistic/request user while reconciling that frame.
    for (const user of existing) {
      if (user.role === 'user' && !used.has(user)) resolved.unshift(user)
    }
  }
  return resolved
}

// A terminal run view is not cleared when the run ends: it survives in the
// session snapshot for the whole state TTL and is replayed on every subscribe.
// Appending from one re-added a days-old round below the newest turns once its
// turn had aged out of the loaded window.
//
// The decision is per turn, not per run. A run owns several turns — an applied
// steer opens its own (SR-TURN-001) — so the run's starting position says
// nothing about where its later turns landed, and using it as the test threw
// away a steer's freshly committed answer along with the aged-out first half.
//
// A turn already on screen always stays: the frame is reconciling it, not
// introducing it. So does a turn whose id the page already shows under another
// role — the frame is completing that turn, not re-introducing a round. A run
// that failed without persisting its error (a retry or edit, or a failure with
// no code) leaves history with the question alone, and the frame is the only
// place the failure reason exists; judging it by position alone left a
// reopened failed session showing the question and no reason for it.
//
// Otherwise the loaded window decides. Positions come from one monotonic
// per-session counter, so a turn numbered at or below the newest settled turn
// is one the history read has already passed: it did not come back, and no
// turn on screen is waiting for it, so this frame has nothing to add. A turn
// numbered past that window, or not numbered at all, is newer than anything
// the read returned and must still be shown.
export function admissibleRuntimeTurns<T extends ChatMessage>(
  messages: readonly ChatMessage[],
  slice: RuntimeTranscriptSlice,
  resolved: T[],
): T[] {
  if (isRuntimeRunActive(slice.status)) return resolved
  let newestSettled: number | undefined
  for (const turn of messages) {
    if (turn.settled !== true || turn.turnPosition === undefined) continue
    if (newestSettled === undefined || turn.turnPosition > newestSettled) {
      newestSettled = turn.turnPosition
    }
  }
  if (newestSettled === undefined) return resolved
  const onScreenTurnIds = new Set<string>()
  for (const turn of messages) {
    const turnId = turn.turnId?.trim()
    if (turnId) onScreenTurnIds.add(turnId)
  }
  const window = newestSettled
  return resolved.filter((turn) => {
    if (messages.includes(turn)) return true
    const turnId = turn.turnId?.trim()
    if (turnId && onScreenTurnIds.has(turnId)) return true
    return turn.turnPosition === undefined || turn.turnPosition > window
  })
}
