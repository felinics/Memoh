import {
  getUsersMeAttentionEvents,
  getUsersMeUnread,
  putBotsByBotIdSessionsBySessionIdRead,
} from '@memohai/sdk'
import { fetchSSEProblem } from './sse-error'

export type AttentionKind = 'reply' | 'failed' | 'needs_input'

/** One unread session as the server reports it. Never carries message text. */
export interface UnreadSessionWire {
  bot_id: string
  session_id: string
  title: string
  attention_kind: AttentionKind
  attention_at: string
}

export type UserAttentionEvent =
  | { type: 'attention_ready' }
  | ({ type: 'session_attention' } & UnreadSessionWire)
  | { type: 'session_read', session_id: string, read_at: string }
  | { type: 'dropped', count?: number }
  | { type: 'ping' }

export async function fetchUnreadSessions(): Promise<UnreadSessionWire[]> {
  const { data } = await getUsersMeUnread({ throwOnError: true })
  return (data?.items ?? []) as UnreadSessionWire[]
}

export async function markSessionRead(botId: string, sessionId: string, through: string): Promise<void> {
  await putBotsByBotIdSessionsBySessionIdRead({
    path: { bot_id: botId, session_id: sessionId },
    body: { through },
    throwOnError: true,
  })
}

/** Runs one subscription of the caller's attention stream until it ends or `signal` aborts. */
export async function streamUserAttentionEvents(
  signal: AbortSignal,
  onEvent: (event: UserAttentionEvent) => void,
): Promise<void> {
  const { stream } = await getUsersMeAttentionEvents({ signal, fetch: fetchSSEProblem, sseMaxRetryAttempts: 1 })
  for await (const payload of stream) {
    if (payload && typeof payload === 'object' && typeof (payload as { type?: unknown }).type === 'string') {
      onEvent(payload as UserAttentionEvent)
    }
  }
}
