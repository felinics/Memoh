import type { ChatAssistantTurn, ChatUserTurn } from './types'
import type { RuntimeTranscriptSlice } from './runtime-projection'

type RuntimeChatTurn = ChatUserTurn | ChatAssistantTurn

export function markRuntimeTurn(
  turn: RuntimeChatTurn,
  slice: RuntimeTranscriptSlice,
  originalUser: boolean,
): RuntimeChatTurn {
  const isSteerAssistantSegment = turn.role === 'assistant' && turn.turnId?.startsWith('queue-steer:')
  if (originalUser || !turn.turnId || (turn.role === 'assistant' && !isSteerAssistantSegment)) {
    turn.turnId = slice.turnId
  }
  turn.runtimeRunId = slice.runId
  if (turn.role === 'user' && slice.continuation) turn.runtimeContinuation = true
  turn.__optimistic = false
  if (turn.role === 'assistant') turn.streaming = slice.streaming
  return turn
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
