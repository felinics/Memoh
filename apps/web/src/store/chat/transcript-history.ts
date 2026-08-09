import type {
  UIMessage,
  UISystemTurn,
  UITurn,
} from '@/composables/api/useChat.types'
import {
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
      messages: (turn.messages ?? []).map(normalizeUIMessage),
      timestamp: normalizeTimestamp(turn.timestamp),
      platform: (turn.platform ?? '').trim() || undefined,
      externalMessageId: (turn.external_message_id ?? '').trim() || undefined,
      streaming: false,
    }
  }

  function normalizeTurns(items: UITurn[], _targetSessionId?: string) {
    const normalized = items.map(normalizeTurn)
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
  // matches twins by entity identity and hands the prior's render key to the
  // incoming twin, so a live → settled handover never remounts the component.
  // The database id survives on serverId for pagination cursors.
  function adoptRenderIdentity(incoming: ChatMessage[]) {
    if (deps.messages.length === 0 || incoming.length === 0) return
    const byIdentity = new Map<string, ChatMessage>()
    for (const existing of deps.messages) {
      const key = turnIdentityKey(existing)
      if (key && !byIdentity.has(key)) byIdentity.set(key, existing)
    }
    for (const twin of incoming) {
      const key = turnIdentityKey(twin)
      if (!key) continue
      const prior = byIdentity.get(key)
      if (!prior || twin.id === prior.id) continue
      twin.serverId = twin.serverId ?? twin.id
      twin.id = prior.id
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

  function replaceMessages(
    items: UITurn[],
    targetSessionId?: string,
    options?: { preserveLive?: boolean },
  ) {
    const next = normalizeTurns(items, targetSessionId)
    adoptRenderIdentity(next)
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
    // The boundary turn is always the newest: one active run per session, and
    // the settled page ends at the last persisted turn.
    deps.messages.splice(0, deps.messages.length, ...next, ...retained)
  }

  function mergeMessages(items: UITurn[], targetSessionId?: string) {
    const incoming = normalizeTurns(items, targetSessionId)
    adoptRenderIdentity(incoming)
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
