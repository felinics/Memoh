// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import type { BotWorkdir } from '@/composables/api/useWorkdirs'
import { clearPersistedUserScopedState, notifyAuthSessionCleared } from '@/lib/auth-session'

/**
 * Failure Modes covered by this behavioral suite:
 * - FM-WD1: `workdirsByBot`, `loadedBots` cache, or `workingWorkdirByBot` survives `notifyAuthSessionCleared`,
 *           exposing Account A's workdirs to Account B or suppressing Account B's `ensureWorkdirs` fetch.
 * - FM-WD2: An in-flight `ensureWorkdirs` request started by Account A resolves AFTER logout and writes
 *           Account A's workdirs into Account B's store because `userScopeGeneration` was not checked.
 * - FM-WD3: `clearPersistedUserScopedState()` / `notifyAuthSessionCleared()` leaves `'bot-order'` or
 *           `'workspace-working-workdir'` behind in `localStorage` or `sessionStorage`.
 */

const fetchWorkdirsMock = vi.fn<(botId: string) => Promise<BotWorkdir[]>>()

vi.mock('@/composables/api/useWorkdirs', () => ({
  fetchWorkdirs: (botId: string) => fetchWorkdirsMock(botId),
}))

function createMemoryStorage(): Storage {
  const data = new Map<string, string>()
  return {
    get length() {
      return data.size
    },
    clear() {
      data.clear()
    },
    getItem(key: string) {
      return data.get(key) ?? null
    },
    key(index: number) {
      return [...data.keys()][index] ?? null
    },
    removeItem(key: string) {
      data.delete(key)
    },
    setItem(key: string, value: string) {
      data.set(key, String(value))
    },
  }
}

const localStorageMem = createMemoryStorage()
const sessionStorageMem = createMemoryStorage()
vi.stubGlobal('localStorage', localStorageMem)
vi.stubGlobal('sessionStorage', sessionStorageMem)

const { useWorkdirsStore } = await import('./workdirs')

function sampleWorkdir(id: string, name: string): BotWorkdir {
  return {
    id,
    bot_id: 'bot-1',
    name,
    path: `/workspace/${id}`,
    target_kind: 'native',
    archived: false,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
  } as BotWorkdir
}

describe('useWorkdirsStore & user-scoped storage cleanup', () => {
  beforeEach(() => {
    fetchWorkdirsMock.mockReset()
    localStorageMem.clear()
    sessionStorageMem.clear()
    setActivePinia(createPinia())
  })

  it('clears in-memory workdirs, loaded cache, and working workdir on auth session clear so the next user refetches cleanly (FM-WD1)', async () => {
    fetchWorkdirsMock.mockResolvedValueOnce([sampleWorkdir('wd-user-a', 'User A Project')])

    const store = useWorkdirsStore()
    await store.ensureWorkdirs('bot-1')
    store.setWorkingWorkdir('bot-1', 'wd-user-a')

    expect(store.workdirsFor('bot-1')).toHaveLength(1)
    expect(store.workingWorkdirId('bot-1')).toBe('wd-user-a')

    // Cached: calling ensureWorkdirs again before logout does not re-fetch.
    await store.ensureWorkdirs('bot-1')
    expect(fetchWorkdirsMock).toHaveBeenCalledTimes(1)

    notifyAuthSessionCleared('logout')

    expect(store.workdirsFor('bot-1')).toEqual([])
    expect(store.workingWorkdirId('bot-1')).toBe('')
    expect(store.workingWorkdirFor('bot-1')).toBeNull()

    // Account B logs in and ensures workdirs for 'bot-1': loadedBots was cleared so fresh data is fetched.
    fetchWorkdirsMock.mockResolvedValueOnce([sampleWorkdir('wd-user-b', 'User B Project')])
    await store.ensureWorkdirs('bot-1')
    expect(fetchWorkdirsMock).toHaveBeenCalledTimes(2)
    expect(store.workdirsFor('bot-1').map(w => w.id)).toEqual(['wd-user-b'])
  })

  it('discards an in-flight Account A workdir load that resolves after auth session clear via userScopeGeneration (FM-WD2)', async () => {
    let resolveAccountAFetch!: (items: BotWorkdir[]) => void
    fetchWorkdirsMock.mockImplementationOnce(() => new Promise<BotWorkdir[]>((resolve) => {
      resolveAccountAFetch = resolve
    }))

    const store = useWorkdirsStore()
    const stalePromise = store.ensureWorkdirs('bot-1')

    // Account A logs out while the request is still in flight.
    notifyAuthSessionCleared('logout')

    // Stale Account A response arrives after logout: must be dropped and must NOT mark 'bot-1' loaded.
    resolveAccountAFetch([sampleWorkdir('wd-stale-a', 'Secret Account A Workdir')])
    await stalePromise

    expect(store.workdirsFor('bot-1')).toEqual([])

    // Subsequent Account B call still fetches cleanly.
    fetchWorkdirsMock.mockResolvedValueOnce([sampleWorkdir('wd-fresh-b', 'Account B Workdir')])
    await store.ensureWorkdirs('bot-1')
    expect(fetchWorkdirsMock).toHaveBeenCalledTimes(2)
    expect(store.workdirsFor('bot-1').map(w => w.id)).toEqual(['wd-fresh-b'])
  })

  it('removes bot-order and workspace-working-workdir from both localStorage and sessionStorage on clearPersistedUserScopedState (FM-WD3)', () => {
    localStorage.setItem('bot-order', JSON.stringify(['bot-9', 'bot-1']))
    sessionStorage.setItem('bot-order', JSON.stringify(['bot-9', 'bot-1']))
    localStorage.setItem('workspace-working-workdir', JSON.stringify({ 'bot-1': 'wd-1' }))
    sessionStorage.setItem('workspace-working-workdir', JSON.stringify({ 'bot-1': 'wd-1' }))

    clearPersistedUserScopedState()

    expect(localStorage.getItem('bot-order')).toBeNull()
    expect(sessionStorage.getItem('bot-order')).toBeNull()
    expect(localStorage.getItem('workspace-working-workdir')).toBeNull()
    expect(sessionStorage.getItem('workspace-working-workdir')).toBeNull()
  })
})
