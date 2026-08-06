import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import type { SessionSummary } from '@/composables/api/useChat.types'

const fetchSessions = vi.fn()

vi.mock('@/composables/api/useChat', () => ({
  get fetchSessions() {
    return fetchSessions
  },
}))

const { createWorkdirSessions } = await import('./workdir-sessions')

function session(id: string, overrides: Partial<SessionSummary> = {}): SessionSummary {
  return {
    id,
    bot_id: 'bot-1',
    title: `Session ${id}`,
    type: 'chat',
    workdir_id: 'workdir-1',
    created_at: '2026-01-02T00:00:00.000Z',
    updated_at: '2026-01-02T00:00:00.000Z',
    ...overrides,
  }
}

function harness() {
  const currentBotId = ref<string | null>('bot-1')
  const sessions = ref<SessionSummary[]>([])
  const remembered = new Map<string, SessionSummary>()
  const store = createWorkdirSessions({
    currentBotId,
    sessions,
    userScopeGeneration: () => 0,
    knownSession: (id: string) => remembered.get(id) ?? null,
    rememberSession: (s: SessionSummary) => { remembered.set(s.id, s) },
  })
  return { currentBotId, sessions, remembered, store }
}

describe('workdir sessions paging', () => {
  beforeEach(() => {
    fetchSessions.mockReset()
  })

  // The whole point of the module: a workdir's chats must be reachable even
  // when they are older than the pages the shared Recents list has loaded.
  it('fetches a folder from the workdir-filtered endpoint', async () => {
    fetchSessions.mockResolvedValue({ items: [session('old-chat')], nextCursor: null })
    const { store, sessions } = harness()

    await store.ensureWorkdirSessions('workdir-1')

    expect(fetchSessions).toHaveBeenCalledWith('bot-1', { workdirId: 'workdir-1' })
    // Not in the shared list at all — it only exists because the folder paged.
    expect(sessions.value).toEqual([])
    expect(store.workdirSessionsFor('workdir-1').map(s => s.id)).toEqual(['old-chat'])
    expect(store.workdirSessionsState('workdir-1')).toEqual({
      loading: false,
      hasMore: false,
      loaded: true,
    })
  })

  it('does not refetch a folder it already loaded', async () => {
    fetchSessions.mockResolvedValue({ items: [session('a')], nextCursor: null })
    const { store } = harness()

    await store.ensureWorkdirSessions('workdir-1')
    await store.ensureWorkdirSessions('workdir-1')

    expect(fetchSessions).toHaveBeenCalledTimes(1)
  })

  it('pages with the cursor and keeps earlier rows', async () => {
    fetchSessions
      .mockResolvedValueOnce({ items: [session('a', { updated_at: '2026-01-03T00:00:00.000Z' })], nextCursor: 'cursor-1' })
      .mockResolvedValueOnce({ items: [session('b', { updated_at: '2026-01-01T00:00:00.000Z' })], nextCursor: null })
    const { store } = harness()

    await store.ensureWorkdirSessions('workdir-1')
    expect(store.workdirSessionsState('workdir-1').hasMore).toBe(true)

    await store.loadMoreWorkdirSessions('workdir-1')

    expect(fetchSessions).toHaveBeenLastCalledWith('bot-1', { workdirId: 'workdir-1', cursor: 'cursor-1' })
    expect(store.workdirSessionsFor('workdir-1').map(s => s.id)).toEqual(['a', 'b'])
    expect(store.workdirSessionsState('workdir-1').hasMore).toBe(false)
  })

  it('merges sessions the shared list already holds, newest first', async () => {
    fetchSessions.mockResolvedValue({
      items: [session('fetched', { updated_at: '2026-01-01T00:00:00.000Z' })],
      nextCursor: null,
    })
    const { store, sessions } = harness()
    sessions.value = [
      session('brand-new', { updated_at: '2026-02-01T00:00:00.000Z' }),
      session('other-workdir', { workdir_id: 'workdir-2' }),
    ]

    await store.ensureWorkdirSessions('workdir-1')

    expect(store.workdirSessionsFor('workdir-1').map(s => s.id)).toEqual(['brand-new', 'fetched'])
  })

  // A fetched row lives in the store's remembered-session map, so the delete
  // path that forgets it there also removes it from the folder.
  it('drops rows the store has forgotten', async () => {
    fetchSessions.mockResolvedValue({ items: [session('a'), session('b')], nextCursor: null })
    const { store, remembered } = harness()

    await store.ensureWorkdirSessions('workdir-1')
    remembered.delete('a')

    expect(store.workdirSessionsFor('workdir-1').map(s => s.id)).toEqual(['b'])
  })

  it('discards a response that lands after a reset', async () => {
    let resolveFetch: (value: unknown) => void = () => {}
    fetchSessions.mockReturnValue(new Promise((resolve) => { resolveFetch = resolve }))
    const { store } = harness()

    const inFlight = store.ensureWorkdirSessions('workdir-1')
    store.reset()
    resolveFetch({ items: [session('stale')], nextCursor: null })
    await inFlight

    expect(store.workdirSessionsFor('workdir-1')).toEqual([])
    expect(store.workdirSessionsState('workdir-1').loaded).toBe(false)
  })
})
