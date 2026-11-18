// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { notifyAuthSessionCleared } from '@/lib/auth-session'

const mocks = vi.hoisted(() => ({
  getPing: vi.fn(),
}))

vi.mock('@memohai/sdk', () => ({
  getPing: mocks.getPing,
}))

import { useCapabilitiesStore } from './capabilities'

afterEach(() => {
  vi.clearAllMocks()
})

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

describe('useCapabilitiesStore', () => {
  it('deduplicates concurrent cold-start load() calls and caches loaded state', async () => {
    setActivePinia(createPinia())
    const store = useCapabilitiesStore()
    const ping = deferred<{ data: Record<string, unknown> }>()
    mocks.getPing.mockReturnValue(ping.promise)

    const pending = Promise.all([store.load(), store.load(), store.load()])
    expect(mocks.getPing).toHaveBeenCalledTimes(1)

    ping.resolve({
      data: {
        snapshot_supported: false,
        connectors: true,
        version: '1.2.3',
        commit_hash: 'abc1234',
      },
    })
    await pending

    expect(store.loaded).toBe(true)
    expect(store.snapshotSupported).toBe(false)
    expect(store.connectors).toBe(true)
    expect(store.serverVersion).toBe('1.2.3')
    expect(store.commitHash).toBe('abc1234')

    await store.load()
    expect(mocks.getPing).toHaveBeenCalledTimes(1)
  })

  it('does not lock loaded=true after /ping failure or empty payload and retries cleanly', async () => {
    setActivePinia(createPinia())
    const store = useCapabilitiesStore()

    mocks.getPing.mockRejectedValueOnce({ status: 502 })
    await expect(store.load()).resolves.toBeUndefined()
    expect(store.loaded).toBe(false)
    expect(store.connectors).toBe(false)
    expect(store.serverVersion).toBe('')

    mocks.getPing.mockResolvedValueOnce({ data: undefined })
    await expect(store.load()).resolves.toBeUndefined()
    expect(store.loaded).toBe(false)

    mocks.getPing.mockResolvedValueOnce({
      data: {
        snapshot_supported: true,
        connectors: true,
        version: '2.0.0',
        commit_hash: 'def5678',
      },
    })
    await store.load()
    expect(mocks.getPing).toHaveBeenCalledTimes(3)
    expect(store.loaded).toBe(true)
    expect(store.connectors).toBe(true)
    expect(store.serverVersion).toBe('2.0.0')
  })

  it('ignores stale in-flight responses across auth session reset', async () => {
    setActivePinia(createPinia())
    const store = useCapabilitiesStore()

    const stalePing = deferred<{ data: Record<string, unknown> }>()
    const freshPing = deferred<{ data: Record<string, unknown> }>()
    mocks.getPing
      .mockReturnValueOnce(stalePing.promise)
      .mockReturnValueOnce(freshPing.promise)

    const firstLoad = store.load()
    expect(mocks.getPing).toHaveBeenCalledTimes(1)

    notifyAuthSessionCleared('logout')

    const secondLoad = store.load()
    expect(mocks.getPing).toHaveBeenCalledTimes(2)

    // Stale response lands first; it must neither apply state nor clear fresh inflight.
    stalePing.resolve({
      data: { snapshot_supported: false, connectors: true, version: 'stale', commit_hash: '111' },
    })
    await firstLoad
    expect(store.loaded).toBe(false)
    expect(store.serverVersion).toBe('')

    // Concurrent third call still shares secondLoad instead of launching a third request.
    const thirdLoad = store.load()
    expect(mocks.getPing).toHaveBeenCalledTimes(2)

    freshPing.resolve({
      data: { snapshot_supported: true, connectors: false, version: 'fresh', commit_hash: '222' },
    })
    await Promise.all([secondLoad, thirdLoad])

    expect(store.loaded).toBe(true)
    expect(store.serverVersion).toBe('fresh')
    expect(store.connectors).toBe(false)
  })
})
