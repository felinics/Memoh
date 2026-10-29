/** Stable deep link shared by the inbox and system notifications, including Electron's memory router. */
export function sessionAttentionPath(botId: string, sessionId: string): string {
  return `/bot/${encodeURIComponent(botId)}?session=${encodeURIComponent(sessionId)}`
}

/**
 * Whether a session can be marked read or unread by this user. Mirrors the server's
 * rule in session_attention.sql: the user's own web chats and external-agent chats,
 * never schedule runs, IM routes or sessions someone else started.
 */
export function canHoldUnread(
  session: { type?: string, channel_type?: string, created_by_user_id?: string },
  userId: string,
): boolean {
  if (!userId || session.created_by_user_id !== userId) return false
  if (session.type !== 'chat' && session.type !== 'acp_agent') return false
  return !session.channel_type || session.channel_type === 'local'
}
