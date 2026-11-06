import type { AttentionKind } from '@/composables/api/attention-api'

/** What opening an attention link should bring into view besides the session. */
export interface AttentionFocus {
  /** The run notified the user (notify_user): scroll to its notice. */
  notice?: boolean
  /** The session is a run of this schedule: reveal it in the schedule panel. */
  scheduleId?: string
}

/** Stable deep link shared by the inbox and system notifications, including Electron's memory router. */
export function sessionAttentionPath(botId: string, sessionId: string, focus: AttentionFocus = {}): string {
  const query = new URLSearchParams({ session: sessionId })
  if (focus.notice) query.set('notice', '1')
  if (focus.scheduleId) query.set('schedule', focus.scheduleId)
  return `/bot/${encodeURIComponent(botId)}?${query.toString()}`
}

/** The focus an attention entry asks for when the user opens it. */
export function attentionFocusOf(entry: { kind: AttentionKind, scheduleId?: string }): AttentionFocus {
  return { notice: entry.kind === 'update', scheduleId: entry.scheduleId }
}

/**
 * Whether a session can be marked read or unread by this user. Mirrors the server's
 * rule in session_attention.sql: the user's own web chats, external-agent chats and
 * schedule runs, never IM routes or sessions someone else started.
 */
export function canHoldUnread(
  session: { type?: string, channel_type?: string, created_by_user_id?: string },
  userId: string,
): boolean {
  if (!userId || session.created_by_user_id !== userId) return false
  if (session.type !== 'chat' && session.type !== 'acp_agent' && session.type !== 'schedule') return false
  return !session.channel_type || session.channel_type === 'local'
}

const URGENCY: Record<AttentionKind, number> = { failed: 3, needs_input: 2, update: 1, reply: 1, marked: 1 }

/**
 * The kind a summary of several unread sessions should speak for: a failure outranks
 * the agent waiting on the user, which outranks an update, a reply or a manual mark.
 */
export function mostUrgentKind(kinds: Iterable<AttentionKind>): AttentionKind | null {
  let top: AttentionKind | null = null
  for (const kind of kinds) {
    if (!top || URGENCY[kind] > URGENCY[top]) top = kind
  }
  return top
}

/** BadgeCount tone for a count of unread sessions: red only for a failure, yellow while one waits on the user, brand otherwise. */
export function attentionBadgeVariant(kinds: Iterable<AttentionKind>): 'destructive' | 'warning' | 'brand' {
  const top = mostUrgentKind(kinds)
  if (top === 'failed') return 'destructive'
  if (top === 'needs_input') return 'warning'
  return 'brand'
}
