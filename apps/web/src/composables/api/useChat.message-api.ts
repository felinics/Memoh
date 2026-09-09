import {
  getBotsByBotIdMessages,
  getBotsByBotIdMessagesLocate,
  getBotsByBotIdSessionsEvents,
  getBotsByBotIdSkillsCatalog,
  postBotsByBotIdQuickActionsExecute,
} from '@memohai/sdk'
import type { ConversationUiTurn } from '@memohai/sdk'
import type {
  BotSessionActivityEvent,
  CommandEventResponse,
  FetchMessagesOptions,
  RequestedSkillSelection,
  UITurn,
} from './useChat.types'

export async function fetchMessagesUI(
  botId: string,
  sessionId: string,
  options?: FetchMessagesOptions,
): Promise<UITurn[]> {
  const sid = sessionId.trim()
  if (!sid) throw new Error('session id is required')
  const { data } = await getBotsByBotIdMessages({
    path: { bot_id: botId },
    query: {
      session_id: sid,
      limit: options?.limit ?? 30,
      ...(options?.beforeMessageId?.trim() ? { before_message_id: options.beforeMessageId.trim() } : {}),
      ...(options?.before?.trim() ? { before: options.before.trim() } : {}),
    },
    throwOnError: true,
  })
  if (!data) throw new Error('messages response body is required')
  return serializeUITurns(data.items)
}

export interface LocateMessageResult {
  items: UITurn[]
  target_id: string
  target_external_message_id: string
}

export async function locateMessageUI(
  botId: string,
  sessionId: string,
  externalMessageId: string,
  before = 30,
  after = 30,
): Promise<LocateMessageResult> {
  const { data } = await getBotsByBotIdMessagesLocate({
    path: { bot_id: botId },
    query: {
      session_id: sessionId,
      external_message_id: externalMessageId,
      before,
      after,
    },
    throwOnError: true,
  })
  if (!data) throw new Error('located message response body is required')
  return {
    items: serializeUITurns(data.items),
    target_id: data.target_id,
    target_external_message_id: data.target_external_message_id,
  }
}

function serializeUITurns(items: ConversationUiTurn[]): UITurn[] {
  return items.map(item => ({
    ...item,
    timestamp: item.timestamp.toISOString(),
  })) as UITurn[]
}

function isCommandEvent(value: unknown): value is CommandEventResponse {
  if (!value || typeof value !== 'object') return false
  const type = String((value as { type?: unknown }).type ?? '').trim()
  return type === 'command_result' || type === 'command_error'
}

export async function fetchSafeSkillCatalog(botId: string): Promise<RequestedSkillSelection[]> {
  const bid = botId.trim()
  if (!bid) return []
  const { data } = await getBotsByBotIdSkillsCatalog({
    path: { bot_id: bid },
    throwOnError: true,
  })
  return (data?.skills ?? []).flatMap((item): RequestedSkillSelection[] => {
    const name = item.name?.trim()
    if (!name) return []
    return [{
      name,
      display_name: item.display_name?.trim() || undefined,
      description: item.description?.trim() || undefined,
      source_kind: item.source_kind?.trim() || undefined,
      state: item.state?.trim() || undefined,
    }]
  })
}

export async function executeQuickAction(
  botId: string,
  actionId: string,
  options: { invocationId?: string; composerScope?: string; sessionId?: string; skillActivationAllowed?: boolean; modeId?: string } = {},
): Promise<CommandEventResponse> {
  const bid = botId.trim()
  const aid = actionId.trim()
  if (!bid) throw new Error('bot id is required')
  if (!aid) throw new Error('action id is required')
  const { data } = await postBotsByBotIdQuickActionsExecute({
    path: { bot_id: bid },
    body: {
      action_id: aid,
      invocation_id: options.invocationId?.trim() || undefined,
      composer_scope: options.composerScope?.trim() || undefined,
      session_id: options.sessionId?.trim() || undefined,
      params: options.skillActivationAllowed === false || options.modeId?.trim()
        ? {
            ...(options.skillActivationAllowed === false ? { skill_activation_allowed: false } : {}),
            ...(options.modeId?.trim() ? { mode_id: options.modeId.trim() } : {}),
          }
        : undefined,
    },
    throwOnError: true,
  })
  if (isCommandEvent(data)) return data
  throw new Error('invalid quick action response')
}

// The SDK's `sse.get` yields parsed `data` payloads from the async generator.
// Wrap each subscription so callers receive typed events and a promise that
// resolves when the stream ends (signal abort or server close).
async function consumeSSE<T extends { type: string }>(
  stream: AsyncGenerator<unknown>,
  isEvent: (value: unknown) => value is T,
  onEvent: (event: T) => void,
): Promise<void> {
  for await (const payload of stream) {
    if (isEvent(payload)) onEvent(payload)
  }
}

function isTypedEvent(value: unknown): value is { type: string } {
  return !!value && typeof value === 'object' && 'type' in value
    && typeof (value as { type: unknown }).type === 'string'
    && (value as { type: string }).type.trim().length > 0
}

export async function streamBotSessionsActivityEvents(
  botId: string,
  signal: AbortSignal,
  onEvent: (event: BotSessionActivityEvent) => void,
): Promise<void> {
  const bid = botId.trim()
  if (!bid) throw new Error('bot id is required')

  const { stream } = await getBotsByBotIdSessionsEvents({
    path: { bot_id: bid },
    signal,
    sseMaxRetryAttempts: 1,
  })

  await consumeSSE(stream, (value): value is BotSessionActivityEvent => isTypedEvent(value), onEvent)
}
