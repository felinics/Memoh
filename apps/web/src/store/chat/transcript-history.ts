import type {
  UIMessage,
  UISystemTurn,
  UITurn,
} from '@/composables/api/useChat.types'
import {
  messageIdentityId,
  nextId,
  normalizeAttachment,
  normalizeForwardRef,
  normalizeReplyRef,
  normalizeTimestamp,
  resolveIsSelf,
  skillActivationTextFromRaw,
  sortChatMessages,
} from '../chat-list.normalize'
import {
  isBackgroundTaskActive,
  normalizeBackgroundTask,
  reconcileBackgroundTasksInMessages,
} from './background-tasks'
import { inheritErrorDetails } from './runtime-transcript-merge'
import type {
  BackgroundTask,
  ChatMessage,
  ContentBlock,
  ToolCallBlock,
} from './types'

export function createTranscriptHistory(deps: {
  messages: ChatMessage[]
  rememberBackgroundTask: (task: BackgroundTask) => BackgroundTask
  applyPendingBackgroundEventsToTool: (block: ToolCallBlock) => void
  // Reports whether the session runtime still has an active run for this
  // turn. Settled reconciliation keeps unpersisted live turns on screen, but
  // a turn whose run ended AND that never landed in the settled page has
  // vanished (aborted before any visible output) and must be dropped, not
  // retained. Without this predicate those two cases are indistinguishable.
  isTurnLive?: (turnId: string) => boolean
}) {
  function normalizeUIMessage(msg: UIMessage): ContentBlock {
    switch (msg.type) {
      case 'tool': {
        const backgroundTask = normalizeBackgroundTask(msg.background_task)
        const block: ToolCallBlock = {
          ...msg,
          toolCallId: msg.tool_call_id,
          toolName: msg.name,
          result: msg.output ?? null,
          running: backgroundTask
            ? isBackgroundTaskActive(backgroundTask)
            : msg.running,
          done: backgroundTask
            ? !isBackgroundTaskActive(backgroundTask)
            : !msg.running,
          approval: msg.approval,
          userInput: msg.user_input,
          backgroundTask: backgroundTask ?? undefined,
          progress: msg.progress ? [...msg.progress] : undefined,
        }
        deps.applyPendingBackgroundEventsToTool(block)
        return block
      }
      case 'attachments':
        return {
          ...msg,
          attachments: msg.attachments.map(normalizeAttachment),
        }
      default:
        return { ...msg }
    }
  }

  function normalizeTurn(turn: UITurn): ChatMessage {
    if (turn.role === 'user') {
      const userMessageKind = (turn.user_message_kind ?? '').trim()
        || (turn.skill_activation ? 'skill_activation' : undefined)
      return {
        id: String(turn.id ?? nextId()),
        turnId: turn.turn_id,
        turnPosition: turn.turn_position ?? undefined,
        role: 'user',
        text: turn.skill_activation
          ? skillActivationTextFromRaw(turn.text ?? '', turn.skill_activation)
          : turn.text ?? '',
        userMessageKind,
        skillActivation: turn.skill_activation,
        attachments: (turn.attachments ?? []).map(normalizeAttachment),
        reply: normalizeReplyRef(turn.reply),
        forward: normalizeForwardRef(turn.forward),
        timestamp: normalizeTimestamp(turn.timestamp),
        platform: (turn.platform ?? '').trim() || undefined,
        senderDisplayName: (turn.sender_display_name ?? '').trim() || undefined,
        senderAvatarUrl: (turn.sender_avatar_url ?? '').trim() || undefined,
        senderUserId: (turn.sender_user_id ?? '').trim() || undefined,
        externalMessageId: (turn.external_message_id ?? '').trim() || undefined,
        streaming: false,
        isSelf: resolveIsSelf(turn),
      }
    }
    if (turn.role === 'system') {
      const task = normalizeBackgroundTask((turn as UISystemTurn).background_task)
        ?? { taskId: String(turn.id ?? nextId()), status: 'completed' }
      const latest = deps.rememberBackgroundTask(task)
      return {
        id: String(turn.id ?? `system-${latest.taskId}`),
        turnId: turn.turn_id,
        turnPosition: turn.turn_position ?? undefined,
        role: 'system',
        kind: 'background_task',
        backgroundTask: latest,
        timestamp: normalizeTimestamp(turn.timestamp),
        platform: (turn.platform ?? '').trim() || undefined,
        streaming: false,
      }
    }
    return {
      id: String(turn.id ?? nextId()),
      turnId: turn.turn_id,
      turnPosition: turn.turn_position ?? undefined,
      role: 'assistant',
      ...(turn.runtime_forkable !== undefined ? { runtimeForkable: turn.runtime_forkable } : {}),
      messages: (turn.messages ?? []).map(normalizeUIMessage),
      timestamp: normalizeTimestamp(turn.timestamp),
      platform: (turn.platform ?? '').trim() || undefined,
      externalMessageId: (turn.external_message_id ?? '').trim() || undefined,
      streaming: false,
    }
  }

  // Every caller of this plural form is reading durable history (the message
  // page, a locate window, an older page). Marking here — rather than inside
  // normalizeTurn, which the runtime projection also uses — is what keeps
  // "came from the database" a fact about the source, not a guess about the
  // turn's shape.
  function normalizeTurns(items: UITurn[], _targetSessionId?: string) {
    const normalized = items.map(normalizeTurn)
    for (const turn of normalized) turn.settled = true
    reconcileBackgroundTasksInMessages(normalized)
    return normalized
  }

  // A turn's entity identity is (turnId, role): the user turn and the
  // assistant turn of one round share turnId, so role must disambiguate.
  function turnIdentityKey(turn: ChatMessage): string {
    if (turn.role === 'system') return ''
    const turnId = turn.turnId?.trim() ?? ''
    return turnId ? `${turnId}:${turn.role}` : ''
  }

  // Render identity (the Vue key) and entity identity (who the turn is) are
  // orthogonal: the render key is born with the on-screen turn and never
  // changes, while the settled twin arrives under the database id. Adoption
  // hands the prior's render key to the incoming twin, so a live → settled
  // handover never remounts the component. The database id survives on
  // serverId for pagination cursors.
  //
  // Matching runs strongest-first. A row the database has named is the same row
  // wherever it appears, so its stored id decides before anything else; only
  // what is left over is paired by (turn_id, role), and only against turns that
  // have no stored id — the live and optimistic ones whose key a settled twin
  // is meant to inherit. A page that repeats a (turn_id, role) would otherwise
  // hand one render key to two different rows: the id-keyed merge collapses
  // them into one, and a refresh that returns only the second overwrites the
  // first's content with it.
  //
  // No adoption may mint a key twice. That is the loss this exists to prevent,
  // so a pairing that would collide is skipped and the twin keeps its own id.
  function adoptRenderIdentity(incoming: ChatMessage[]) {
    if (deps.messages.length === 0 || incoming.length === 0) return
    const byStoredId = new Map<string, ChatMessage>()
    const unnamedByIdentity = new Map<string, ChatMessage[]>()
    for (const existing of deps.messages) {
      const storedId = existing.settled === true ? messageIdentityId(existing) : ''
      if (storedId) {
        if (!byStoredId.has(storedId)) byStoredId.set(storedId, existing)
        continue
      }
      const key = turnIdentityKey(existing)
      if (!key) continue
      const unnamed = unnamedByIdentity.get(key)
      if (unnamed) unnamed.push(existing)
      else unnamedByIdentity.set(key, [existing])
    }

    const claimed = new Set<string>()
    const unmatched: ChatMessage[] = []
    for (const twin of incoming) {
      const prior = byStoredId.get(messageIdentityId(twin))
      if (!prior) {
        unmatched.push(twin)
        continue
      }
      claimed.add(prior.id)
      if (twin.id === prior.id) continue
      twin.serverId = twin.serverId ?? twin.id
      twin.id = prior.id
    }
    for (const twin of unmatched) {
      const key = turnIdentityKey(twin)
      if (!key) continue
      const prior = unnamedByIdentity.get(key)?.shift()
      if (!prior || twin.id === prior.id || claimed.has(prior.id)) continue
      claimed.add(prior.id)
      twin.serverId = twin.serverId ?? twin.id
      twin.id = prior.id
    }
  }

  // History stores a failure's code alone; a settled twin keeps the args and
  // copy the live error frame left on the turn it replaces.
  function inheritLiveErrorDetails(incoming: ChatMessage[]) {
    const byId = new Map(deps.messages.map(turn => [turn.id, turn]))
    for (const twin of incoming) {
      const prior = byId.get(twin.id)
      if (twin.role === 'assistant' && prior?.role === 'assistant') inheritErrorDetails(twin, prior)
    }
  }

  // An on-screen turn survives a settled replacement only while it is the
  // moving boundary: a local optimistic turn the server has not named yet, or
  // backed by a run the runtime still reports as active. A streaming flag
  // alone does not retain: a failed turn can stay streaming:true forever, and
  // retaining it would resurrect a zombie the settled page already rejected.
  // Anything else not present in the settled page is either already settled
  // (its twin replaces it) or vanished, and must not linger.
  function isLiveBoundaryTurn(turn: ChatMessage): boolean {
    if (turn.role === 'system') return false
    const turnId = turn.turnId?.trim() ?? ''
    // No turnId yet: a local optimistic turn mid-send that the server has not
    // named. The settled page cannot know it, so it is always retained.
    if (!turnId) return turn.__optimistic === true
    return deps.isTurnLive?.(turnId) === true
  }

  // Retained turns are not always the newest: a channel message persisted while
  // a run streams takes a later position than the run's own turn, so appending
  // the live turn rendered the reply below a request that came after it.
  //
  // The settled page arrives in the server's authoritative order and is left
  // exactly as delivered — re-sorting it would reorder rows whenever the sort
  // key is degenerate, which is how a whole page can be shuffled by a tie. Each
  // retained turn is inserted before the first settled turn numbered past it;
  // one with no number goes to the tail, because that is where the database
  // will number it.
  function mergeRetainedByPosition(next: ChatMessage[], retained: ChatMessage[]): ChatMessage[] {
    if (retained.length === 0) return next
    const merged = [...next]
    for (const turn of retained) {
      const position = turn.turnPosition
      const index = position === undefined
        ? -1
        : merged.findIndex(settled =>
            settled.turnPosition !== undefined && settled.turnPosition > position)
      if (index < 0) merged.push(turn)
      else merged.splice(index, 0, turn)
    }
    return merged
  }

  function replaceMessages(
    items: UITurn[],
    targetSessionId?: string,
    options?: { preserveLive?: boolean },
  ) {
    const next = normalizeTurns(items, targetSessionId)
    adoptRenderIdentity(next)
    inheritLiveErrorDetails(next)
    if (options?.preserveLive === false) {
      deps.messages.splice(0, deps.messages.length, ...next)
      return
    }
    const settledKeys = new Set(
      next.map(turnIdentityKey).filter(key => key !== ''),
    )
    const retained = deps.messages.filter(turn =>
      isLiveBoundaryTurn(turn) && !settledKeys.has(turnIdentityKey(turn)),
    )
    deps.messages.splice(0, deps.messages.length, ...mergeRetainedByPosition(next, retained))
  }

  function mergeMessages(items: UITurn[], targetSessionId?: string) {
    const incoming = normalizeTurns(items, targetSessionId)
    adoptRenderIdentity(incoming)
    inheritLiveErrorDetails(incoming)
    const merged = new Map<string, ChatMessage>()
    for (const item of deps.messages) merged.set(item.id, item)
    for (const item of incoming) merged.set(item.id, item)
    deps.messages.splice(
      0,
      deps.messages.length,
      ...sortChatMessages([...merged.values()]),
    )
  }

  return {
    normalizeUIMessage,
    normalizeTurn,
    normalizeTurns,
    replaceMessages,
    mergeMessages,
  }
}
