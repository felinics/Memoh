import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import type { UserAttentionEvent } from '@/composables/api/attention-api'

const api = vi.hoisted(() => ({
  fetchUnreadSessions: vi.fn(),
  setReadState: vi.fn(),
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
    api.setReadState.mockReset().mockResolvedValue([])
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
    expect(live).toHaveBeenCalledWith({ sessionId: 's1', botId: 'bot-a', title: 'Refactor login', kind: 'reply', attentionAt: reply.attention_at, readThrough: reply.attention_at, marked: false })
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

    expect(api.setReadState).toHaveBeenCalledWith([{ session_id: 's1', unread: false, through: reply.attention_at }])
    expect(store.isUnread('s1')).toBe(false)
  })

  it('does not drop attention that arrived while mark-read was in flight', async () => {
    api.fetchUnreadSessions.mockResolvedValue([reply])
    const stream = captureStream()
    let resolveRead: () => void = () => {}
    api.setReadState.mockImplementation(() => new Promise((resolve) => { resolveRead = () => resolve([]) }))
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

  it('preserves live attention while the initial snapshot is in flight', async () => {
    let resolveSnapshot!: (items: typeof reply[]) => void
    api.fetchUnreadSessions.mockImplementation(() => new Promise(resolve => { resolveSnapshot = resolve }))
    const stream = captureStream()
    const store = useUnreadStore()
    store.start()
    stream.emit({ type: 'session_attention', ...reply })
    resolveSnapshot([])
    await flush()
    expect(store.isUnread('s1')).toBe(true)
  })

  it('does not resurrect a read session from an in-flight snapshot or replay', async () => {
    let resolveSnapshot!: (items: typeof reply[]) => void
    api.fetchUnreadSessions.mockImplementation(() => new Promise(resolve => { resolveSnapshot = resolve }))
    const stream = captureStream()
    const store = useUnreadStore()
    const live = vi.fn()
    store.onAttention(live)
    store.start()
    stream.emit({ type: 'session_read', session_id: 's1', read_at: reply.attention_at })
    resolveSnapshot([reply])
    await flush()
    stream.emit({ type: 'session_attention', ...reply })
    expect(store.total).toBe(0)
    expect(live).not.toHaveBeenCalled()
  })

  it('ignores duplicate and older attention instead of notifying twice', async () => {
    const stream = captureStream()
    const store = useUnreadStore()
    const live = vi.fn()
    store.onAttention(live)
    store.start()
    await flush()
    stream.emit({ type: 'session_attention', ...reply })
    stream.emit({ type: 'session_attention', ...reply })
    stream.emit({ type: 'session_attention', ...reply, attention_at: '2026-10-16T07:59:00Z' })
    expect(live).toHaveBeenCalledTimes(1)
    expect(store.inbox[0]?.attentionAt).toBe(reply.attention_at)
  })

  it('treats only a just-arrived live attention as fresh', async () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    try {
      vi.setSystemTime(new Date('2026-10-29T08:00:00Z'))
      api.fetchUnreadSessions.mockResolvedValue([{ ...reply, session_id: 's0' }])
      const stream = captureStream()
      const store = useUnreadStore()
      store.start()
      await flush()
      stream.emit({ type: 'session_attention', ...reply })

      expect(store.isFresh('s0', reply.attention_at)).toBe(false)
      expect(store.isFresh('s1', reply.attention_at)).toBe(true)
      expect(store.isFresh('s1', '2026-10-16T07:00:00Z')).toBe(false)

      vi.setSystemTime(new Date('2026-10-29T08:00:05Z'))
      expect(store.isFresh('s1', reply.attention_at)).toBe(false)
    }
    finally {
      vi.useRealTimers()
    }
  })

  it('reads a manually marked session through its mark, not its older attention', async () => {
    const marked = { ...reply, read_through: '2026-10-16T09:30:00Z', marked: true }
    api.fetchUnreadSessions.mockResolvedValue([marked])
    captureStream()
    const store = useUnreadStore()
    store.start()
    await flush()

    expect(store.entries.s1?.marked).toBe(true)
    await store.markRead('s1')

    expect(api.setReadState).toHaveBeenCalledWith([{ session_id: 's1', unread: false, through: marked.read_through }])
    expect(store.isUnread('s1')).toBe(false)
  })

  it('marks sessions unread without notifying', async () => {
    captureStream()
    const store = useUnreadStore()
    const live = vi.fn()
    store.onAttention(live)
    store.start()
    await flush()
    api.setReadState.mockResolvedValue([{ ...reply, read_through: '2026-10-16T09:30:00Z', marked: true }])

    await store.markUnread(['s1'])

    expect(api.setReadState).toHaveBeenCalledWith([{ session_id: 's1', unread: true }])
    expect(store.isUnread('s1')).toBe(true)
    expect(store.entries.s1?.marked).toBe(true)
    expect(live).not.toHaveBeenCalled()
  })

  it('shows a session another tab marked unread without notifying', async () => {
    const stream = captureStream()
    const store = useUnreadStore()
    const live = vi.fn()
    store.onAttention(live)
    store.start()
    await flush()

    stream.emit({ type: 'session_unread', ...reply, read_through: '2026-10-16T09:30:00Z', marked: true })

    expect(store.isUnread('s1')).toBe(true)
    expect(live).not.toHaveBeenCalled()
    expect(store.isFresh('s1', reply.attention_at)).toBe(false)
  })

  it('reads everything at once and can bring it back', async () => {
    const second = { ...reply, session_id: 's2', attention_at: '2026-10-16T09:00:00Z' }
    api.fetchUnreadSessions.mockResolvedValue([reply, second])
    captureStream()
    const store = useUnreadStore()
    store.start()
    await flush()

    const cleared = await store.markAllRead()

    expect(cleared.sort()).toEqual(['s1', 's2'])
    expect(store.total).toBe(0)
    expect(api.setReadState).toHaveBeenCalledWith(expect.arrayContaining([
      { session_id: 's1', unread: false, through: reply.attention_at },
      { session_id: 's2', unread: false, through: second.attention_at },
    ]))

    api.setReadState.mockResolvedValue([{ ...reply, read_through: '2026-10-16T10:00:00Z', marked: true }, { ...second, read_through: '2026-10-16T10:00:00Z', marked: true }])
    await store.markUnread(cleared)
    expect(store.total).toBe(2)
  })

  it('restores the inbox when reading everything fails', async () => {
    api.fetchUnreadSessions.mockResolvedValue([reply])
    captureStream()
    const store = useUnreadStore()
    store.start()
    await flush()
    api.setReadState.mockRejectedValue(new Error('offline'))

    await expect(store.markAllRead()).rejects.toThrow('offline')
    expect(store.isUnread('s1')).toBe(true)
  })
})
