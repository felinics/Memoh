import {
  getUsersMeAttentionEvents,
  getUsersMeUnread,
  putUsersMeReadState,
} from '@memohai/sdk'
import { fetchSSEProblem } from './sse-error'

export type AttentionKind = 'reply' | 'failed' | 'needs_input' | 'marked'

/** One unread session as the server reports it. Never carries message text. */
export interface UnreadSessionWire {
  bot_id: string
  session_id: string
  title: string
  attention_kind: AttentionKind
  attention_at: string
  /** What reading it must cover; later than attention_at when it was marked unread. */
  read_through?: string
  /** The user marked it unread themselves. */
  marked?: boolean
}

/** One session's desired read state; `through` is required to read. */
export interface ReadStateChange {
  session_id: string
  unread: boolean
  through?: string
}

export type UserAttentionEvent =
  | { type: 'attention_ready' }
  | ({ type: 'session_attention' } & UnreadSessionWire)
  | ({ type: 'session_unread' } & UnreadSessionWire)
  | { type: 'session_read', session_id: string, read_at: string }
  | { type: 'dropped', count?: number }
  | { type: 'ping' }

export async function fetchUnreadSessions(): Promise<UnreadSessionWire[]> {
  const { data } = await getUsersMeUnread({ throwOnError: true })
  return (data?.items ?? []) as UnreadSessionWire[]
}

/** Writes read or unread for each session; returns the sessions it left unread. */
export async function setReadState(changes: ReadStateChange[]): Promise<UnreadSessionWire[]> {
  const { data } = await putUsersMeReadState({ body: { items: changes }, throwOnError: true })
  return (data?.items ?? []) as UnreadSessionWire[]
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
