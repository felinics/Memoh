import { describe, expect, it, vi } from 'vitest'
import { createAttentionNotifications } from './attention-notifications'

const payload = {
  botId: 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
  sessionId: 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb',
  attentionAt: '2026-10-20T08:00:00Z',
  title: 'Atlas · Refactor',
  body: 'Reply ready',
}

describe('desktop attention notifications', () => {
  it('deduplicates renderers, replaces a session notification and navigates on click', () => {
    const clicks: Array<() => void> = []
    const close = vi.fn()
    const show = vi.fn((_p, onClick: () => void) => { clicks.push(onClick); return close })
    const navigate = vi.fn()
    const notifications = createAttentionNotifications({ show, navigate })
    notifications.notify(payload)
    notifications.notify(payload)
    expect(show).toHaveBeenCalledTimes(1)
    clicks[0]?.()
    expect(navigate).toHaveBeenCalledWith(`/bot/${payload.botId}?session=${payload.sessionId}`)
    notifications.notify({ ...payload, attentionAt: '2026-10-20T08:01:00Z' })
    expect(close).toHaveBeenCalledTimes(1)
    expect(show).toHaveBeenCalledTimes(2)
    notifications.clear()
    expect(close).toHaveBeenCalledTimes(2)
  })

  it('rejects invalid IPC input and stale events', () => {
    const show = vi.fn(() => vi.fn())
    const notifications = createAttentionNotifications({ show, navigate: vi.fn() })
    for (const invalid of [null, {}, { ...payload, botId: '//evil.test' }, { ...payload, attentionAt: 'bad' }, { ...payload, title: 'x'.repeat(1000) }]) {
      notifications.notify(invalid)
    }
    expect(show).not.toHaveBeenCalled()
    notifications.notify(payload)
    notifications.notify({ ...payload, attentionAt: '2026-10-19T08:00:00Z' })
    expect(show).toHaveBeenCalledTimes(1)
  })
})
