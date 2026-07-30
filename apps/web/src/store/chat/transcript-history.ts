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

  function adoptRenderIdentity(incoming: ChatMessage[]) {
    if (deps.messages.length === 0 || incoming.length === 0) return
    const byServerId = new Map<string, ChatMessage>()
    for (const existing of deps.messages) {
      if (existing.serverId) byServerId.set(existing.serverId, existing)
    }
    for (const twin of incoming) {
      const prior = byServerId.get(twin.serverId ?? twin.id)
      if (!prior || twin.id === prior.id) continue
      twin.serverId = twin.serverId ?? twin.id
      twin.id = prior.id
    }
  }

  function replaceMessages(items: UITurn[], targetSessionId?: string) {
    const next = normalizeTurns(items, targetSessionId)
    adoptRenderIdentity(next)
    deps.messages.splice(0, deps.messages.length, ...next)
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
