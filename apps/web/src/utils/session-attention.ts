/** Stable deep link shared by the inbox and system notifications, including Electron's memory router. */
export function sessionAttentionPath(botId: string, sessionId: string): string {
  return `/bot/${encodeURIComponent(botId)}?session=${encodeURIComponent(sessionId)}`
}
