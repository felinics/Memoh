import type { AttentionNotification } from '../shared/attention'

const uuid = /^[\da-f]{8}(?:-[\da-f]{4}){3}-[\da-f]{12}$/i

function parseNotification(value: unknown): AttentionNotification | null {
  if (!value || typeof value !== 'object') return null
  const data = value as Record<string, unknown>
  if (typeof data.botId !== 'string' || !uuid.test(data.botId)
    || typeof data.sessionId !== 'string' || !uuid.test(data.sessionId)
    || typeof data.attentionAt !== 'string' || !Number.isFinite(Date.parse(data.attentionAt))
    || typeof data.title !== 'string' || data.title.length > 256
    || typeof data.body !== 'string' || data.body.length > 1024
    || (data.notice !== undefined && typeof data.notice !== 'boolean')
    || (data.scheduleId !== undefined && (typeof data.scheduleId !== 'string' || !uuid.test(data.scheduleId)))) return null
  return data as unknown as AttentionNotification
}

/** The route a notification opens, built here from validated ids only. */
function notificationPath(payload: AttentionNotification): string {
  const query = new URLSearchParams({ session: payload.sessionId })
  if (payload.notice) query.set('notice', '1')
  if (payload.scheduleId) query.set('schedule', payload.scheduleId)
  return `/bot/${payload.botId}?${query.toString()}`
}

/** The main process owns deduplication across windows; no renderer-provided URL is trusted. */
export function createAttentionNotifications(deps: {
  show(payload: AttentionNotification, onClick: () => void): (() => void) | null
  navigate(path: string): void
}) {
  const latest = new Map<string, { at: number, close: () => void }>()

  function notify(raw: unknown) {
    const payload = parseNotification(raw)
    if (!payload) return
    const at = Date.parse(payload.attentionAt)
    const previous = latest.get(payload.sessionId)
    if (previous && previous.at >= at) return
    const close = deps.show(payload, () => deps.navigate(notificationPath(payload)))
    if (!close) return
    previous?.close()
    latest.delete(payload.sessionId)
    latest.set(payload.sessionId, { at, close })
    if (latest.size > 1000) {
      const oldest = latest.keys().next().value
      if (oldest) {
        latest.get(oldest)?.close()
        latest.delete(oldest)
      }
    }
  }

  function clear() {
    for (const entry of latest.values()) entry.close()
    latest.clear()
  }

  return { notify, clear }
}
