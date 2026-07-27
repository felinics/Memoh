import type { Ref } from 'vue'
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
  ChatAssistantTurn,
  ChatMessage,
  ChatUserTurn,
  ContentBlock,
  ToolCallBlock,
} from './types'

interface EphemeralAssistantError {
  content: string
  timestamp: string
  userText?: string
}

export function createTranscriptHistory(deps: {
  messages: ChatMessage[]
  sessionId: Ref<string | null>
  rememberBackgroundTask: (task: BackgroundTask) => BackgroundTask
  applyPendingBackgroundEventsToTool: (block: ToolCallBlock) => void
  onSnapshot: (sessionId: string | undefined, turns: UITurn[]) => void
  nextAssistantMessageId: (turn: ChatAssistantTurn) => number
}) {
  const ephemeralAssistantErrors = new Map<string, EphemeralAssistantError[]>()

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
      role: 'assistant',
      messages: (turn.messages ?? []).map(normalizeUIMessage),
      timestamp: normalizeTimestamp(turn.timestamp),
      platform: (turn.platform ?? '').trim() || undefined,
      externalMessageId: (turn.external_message_id ?? '').trim() || undefined,
      streaming: false,
    }
  }

  function ephemeralErrorId(sessionId: string, error: EphemeralAssistantError) {
    let hash = 0
    const input = `${error.timestamp}:${error.content}`
    for (let i = 0; i < input.length; i += 1) {
      hash = ((hash << 5) - hash + input.charCodeAt(i)) | 0
    }
    return `ephemeral-error-${sessionId}-${Math.abs(hash).toString(36)}`
  }

  function hasAssistantError(items: ChatMessage[], text: string) {
    return items.some(item =>
      item.role === 'assistant'
      && item.messages.some(block =>
        block.type === 'error' && block.content === text),
    )
  }

  function assistantBeforeTimestamp(items: ChatMessage[], timestamp: string) {
    const errorTime = Date.parse(timestamp)
    let target: ChatAssistantTurn | null = null
    for (const item of items) {
      const itemTime = Date.parse(item.timestamp)
      if (
        !Number.isNaN(errorTime)
        && !Number.isNaN(itemTime)
        && itemTime > errorTime
      ) break
      if (item.role === 'user') target = null
      else if (item.role === 'assistant') target = item
    }
    return target
  }

  function userBeforeAssistant(assistant: ChatAssistantTurn): ChatUserTurn | null {
    const index = deps.messages.indexOf(assistant)
    if (index < 0) return null
    for (let offset = index - 1; offset >= 0; offset -= 1) {
      const item = deps.messages[offset]
      if (item?.role === 'user') return item
    }
    return null
  }

  function anchorUserIndex(items: ChatMessage[], error: EphemeralAssistantError) {
    const targetText = (error.userText ?? '').trim()
    let fallback = -1
    for (let index = items.length - 1; index >= 0; index -= 1) {
      const item = items[index]
      if (item?.role !== 'user') continue
      if (fallback < 0) fallback = index
      if (targetText && item.text.trim() === targetText) return index
    }
    return fallback
  }

  function assistantAfterAnchor(items: ChatMessage[], anchorIndex: number) {
    let target: ChatAssistantTurn | null = null
    for (let index = anchorIndex + 1; index < items.length; index += 1) {
      const item = items[index]
      if (!item) continue
      if (item.role === 'user') break
      if (item.role === 'assistant') target = item
    }
    return target
  }

  function appendEphemeralErrors(items: ChatMessage[], targetSessionId?: string) {
    const sessionId = (targetSessionId ?? deps.sessionId.value ?? '').trim()
    const errors = ephemeralAssistantErrors.get(sessionId)
    if (!sessionId || !errors?.length) return
    for (const error of errors) {
      const text = error.content.trim()
      if (!text || hasAssistantError(items, text)) continue
      const anchorIndex = anchorUserIndex(items, error)
      const assistant = anchorIndex >= 0
        ? assistantAfterAnchor(items, anchorIndex)
        : assistantBeforeTimestamp(items, error.timestamp)
      if (assistant) {
        assistant.messages.push({
          id: deps.nextAssistantMessageId(assistant),
          type: 'error',
          content: text,
        })
        continue
      }
      const insertAt = anchorIndex >= 0 ? anchorIndex + 1 : items.length
      const parsed = Date.parse(items[anchorIndex]?.timestamp ?? '')
      const timestamp = Number.isNaN(parsed)
        ? error.timestamp
        : new Date(parsed + 1).toISOString()
      items.splice(insertAt, 0, {
        id: ephemeralErrorId(sessionId, error),
        role: 'assistant',
        messages: [{ id: 0, type: 'error', content: text }],
        timestamp,
        streaming: false,
      })
    }
  }

  function normalizeTurns(items: UITurn[], targetSessionId?: string) {
    const normalized = items.map(normalizeTurn)
    reconcileBackgroundTasksInMessages(normalized)
    appendEphemeralErrors(normalized, targetSessionId)
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
    deps.onSnapshot(targetSessionId, items)
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

  function rememberAssistantError(
    errorMessage: string,
    targetSessionId: string,
    assistantTurn: ChatAssistantTurn,
  ) {
    const sessionId = targetSessionId.trim()
    const content = errorMessage.trim()
    if (!sessionId || !content) return
    const errors = ephemeralAssistantErrors.get(sessionId) ?? []
    if (errors.some(error => error.content === content)) return
    const userTurn = userBeforeAssistant(assistantTurn)
    errors.push({
      content,
      timestamp: assistantTurn.timestamp,
      userText: userTurn?.text,
    })
    ephemeralAssistantErrors.set(sessionId, errors)
  }

  return {
    normalizeUIMessage,
    normalizeTurn,
    normalizeTurns,
    replaceMessages,
    mergeMessages,
    rememberAssistantError,
    reset: () => { ephemeralAssistantErrors.clear() },
  }
}
