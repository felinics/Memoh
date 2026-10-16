import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import type { UserAttentionEvent } from '@/composables/api/attention-api'

const api = vi.hoisted(() => ({
  fetchUnreadSessions: vi.fn(),
  markSessionRead: vi.fn(),
  streamUserAttentionEvents: vi.fn(),
}))

vi.mock('@/composables/api/attention-api', () => api)

import { useUnreadStore } from './unread'

type Emit = (event: UserAttentionEvent) => void

/** Captures the live stream's event callback so a test can push frames. */
function captureStream(): { emit: Emit, attempts: () => number } {
  let emit: Emit = () => {}
  let attempts = 0
  api.streamUserAttentionEvents.mockImplementation((signal: AbortSignal, onEvent: Emit) => {
    attempts += 1
    emit = onEvent
    return new Promise<void>(resolve => signal.addEventListener('abort', () => resolve()))
  })
  return { emit: e => emit(e), attempts: () => attempts }
}

const flush = () => new Promise(resolve => setTimeout(resolve, 0))

const reply = {
  bot_id: 'bot-a', session_id: 's1', title: 'Refactor login', attention_kind: 'reply', attention_at: '2026-10-16T08:00:00Z',
} as const

describe('unread store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    api.fetchUnreadSessions.mockReset().mockResolvedValue([])
    api.markSessionRead.mockReset().mockResolvedValue(undefined)
    api.streamUserAttentionEvents.mockReset()
  })

  afterEach(() => {
    useUnreadStore().stop()
  })

  it('seeds from the unread summary without firing live listeners', async () => {
    api.fetchUnreadSessions.mockResolvedValue([reply, { ...reply, bot_id: 'bot-b', session_id: 's2', attention_at: '2026-10-16T09:00:00Z' }])
    captureStream()
    const store = useUnreadStore()
    const live = vi.fn()
    store.onAttention(live)

    store.start()
    await flush()

    expect(store.total).toBe(2)
    expect(store.countByBot).toEqual({ 'bot-a': 1, 'bot-b': 1 })
    expect(store.inbox.map(e => e.sessionId)).toEqual(['s2', 's1'])
    expect(live).not.toHaveBeenCalled()
  })

  it('adds live attention and tells listeners', async () => {
    const stream = captureStream()
    const store = useUnreadStore()
    const live = vi.fn()
    store.onAttention(live)
    store.start()
    await flush()

    stream.emit({ type: 'session_attention', ...reply })

    expect(store.isUnread('s1')).toBe(true)
    expect(store.kindOf('s1')).toBe('reply')
    expect(live).toHaveBeenCalledWith({ sessionId: 's1', botId: 'bot-a', title: 'Refactor login', kind: 'reply', attentionAt: reply.attention_at })
  })

  it('clears an entry when another tab reads it', async () => {
    api.fetchUnreadSessions.mockResolvedValue([reply])
    const stream = captureStream()
    const store = useUnreadStore()
    store.start()
    await flush()

    stream.emit({ type: 'session_read', session_id: 's1', read_at: reply.attention_at })

    expect(store.isUnread('s1')).toBe(false)
  })

  it('keeps the entry when the read cursor is older than its attention', async () => {
    api.fetchUnreadSessions.mockResolvedValue([reply])
    const stream = captureStream()
    const store = useUnreadStore()
    store.start()
    await flush()

    stream.emit({ type: 'session_read', session_id: 's1', read_at: '2026-10-16T07:59:00Z' })

    expect(store.isUnread('s1')).toBe(true)
  })

  it('refetches the summary after dropped events', async () => {
    const stream = captureStream()
    const store = useUnreadStore()
    store.start()
    await flush()
    api.fetchUnreadSessions.mockResolvedValue([reply])

    stream.emit({ type: 'dropped', count: 3 })
    await flush()

    expect(api.fetchUnreadSessions).toHaveBeenCalledTimes(2)
    expect(store.isUnread('s1')).toBe(true)
  })

  it('marks read through the attention time it showed', async () => {
    api.fetchUnreadSessions.mockResolvedValue([reply])
    captureStream()
    const store = useUnreadStore()
    store.start()
    await flush()

    await store.markRead('s1')

    expect(api.markSessionRead).toHaveBeenCalledWith('bot-a', 's1', reply.attention_at)
    expect(store.isUnread('s1')).toBe(false)
  })

  it('does not drop attention that arrived while mark-read was in flight', async () => {
    api.fetchUnreadSessions.mockResolvedValue([reply])
    const stream = captureStream()
    let resolveRead: () => void = () => {}
    api.markSessionRead.mockImplementation(() => new Promise<void>((resolve) => { resolveRead = resolve }))
    const store = useUnreadStore()
    store.start()
    await flush()

    const reading = store.markRead('s1')
    stream.emit({ type: 'session_attention', ...reply, attention_kind: 'failed', attention_at: '2026-10-16T08:05:00Z' })
    resolveRead()
    await reading

    expect(store.kindOf('s1')).toBe('failed')
  })


  it('reset empties everything', async () => {
    api.fetchUnreadSessions.mockResolvedValue([reply])
    captureStream()
    const store = useUnreadStore()
    store.start()
    await flush()

    store.reset()

    expect(store.total).toBe(0)
  })
})
