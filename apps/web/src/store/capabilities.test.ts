// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, type Component } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { PiniaColada, useQueryCache } from '@pinia/colada'
import { clearQueryCacheAndPersistence } from '@/lib/query-cache-persistence'
import { notifyAuthSessionCleared } from '@/lib/auth-session'

const mocks = vi.hoisted(() => ({
  getPing: vi.fn(),
}))

vi.mock('@memohai/sdk', () => ({
  getPing: mocks.getPing,
}))

import { useCapabilitiesStore } from './capabilities'

let unmount = () => {}

afterEach(() => {
  unmount()
  unmount = () => {}
  vi.clearAllMocks()
})

function mountWithPiniaColada() {
  const pinia = createPinia()
  let store!: ReturnType<typeof useCapabilitiesStore>
  let queryCache!: ReturnType<typeof useQueryCache>

  const Host: Component = {
    setup() {
      queryCache = useQueryCache()
      store = useCapabilitiesStore()
      return () => h('div')
    },
  }

  const app = createApp(Host)
  app.use(pinia)
  app.use(PiniaColada)
  const el = document.createElement('div')
  app.mount(el)
  unmount = () => app.unmount()

  return { store, queryCache }
}

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
  it('deduplicates concurrent cold-start load() calls with PiniaColada', async () => {
    const ping = deferred<{ data: Record<string, unknown> }>()
    mocks.getPing.mockReturnValue(ping.promise)

    const { store } = mountWithPiniaColada()

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
    const { store } = mountWithPiniaColada()

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

  it('resets state and re-fetches after query cache clear and auth session reset', async () => {
    const { store, queryCache } = mountWithPiniaColada()
    mocks.getPing
      .mockResolvedValueOnce({ data: { connectors: true, version: '1.0.0', commit_hash: 'aaa' } })
      .mockResolvedValueOnce({ data: { connectors: false, version: '2.0.0', commit_hash: 'bbb' } })

    await store.load()
    expect(store.loaded).toBe(true)
    expect(store.serverVersion).toBe('1.0.0')

    clearQueryCacheAndPersistence(queryCache)
    notifyAuthSessionCleared('logout')

    expect(store.loaded).toBe(false)
    expect(store.connectors).toBe(false)
    expect(store.serverVersion).toBe('')

    await store.load()
    expect(mocks.getPing).toHaveBeenCalledTimes(2)
    expect(store.loaded).toBe(true)
    expect(store.serverVersion).toBe('2.0.0')
  })

  it('deduplicates, retries, and resets under isolated Pinia without PiniaColada', async () => {
    setActivePinia(createPinia())
    const store = useCapabilitiesStore()

    const ping = deferred<{ data: Record<string, unknown> }>()
    mocks.getPing.mockReturnValueOnce(ping.promise)
    const p = Promise.all([store.load(), store.load()])
    expect(mocks.getPing).toHaveBeenCalledTimes(1)
    ping.reject(new Error('502'))
    await p
    expect(store.loaded).toBe(false)

    mocks.getPing.mockResolvedValueOnce({
      data: { snapshot_supported: false, connectors: true, version: '3.0.0', commit_hash: 'ccc' },
    })
    await store.load()
    expect(store.loaded).toBe(true)
    expect(store.snapshotSupported).toBe(false)
    expect(store.serverVersion).toBe('3.0.0')

    notifyAuthSessionCleared('logout')
    expect(store.loaded).toBe(false)
    expect(store.snapshotSupported).toBe(true)
    expect(store.serverVersion).toBe('')
  })
})
