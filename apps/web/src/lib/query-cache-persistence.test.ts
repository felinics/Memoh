import { afterEach, describe, expect, it, vi } from 'vitest'
import { createPinia } from 'pinia'
import { useQueryCache } from '@pinia/colada'
import type { QueryCache, UseQueryEntry } from '@pinia/colada'
import {
  QUERY_CACHE_STORAGE_KEY,
  cancelPendingQueryCacheSave,
  clearQueryCacheAndPersistence,
  createQueryCachePersistencePlugin,
  persistableQueryFilter,
  removeQueryCacheFromDisk,
  saveQueryCacheToDiskNow,
} from './query-cache-persistence'

function entryWith(
  key: readonly unknown[],
  status: 'success' | 'pending' | 'error' = 'success',
  data: unknown = { id: 'x' },
) {
  return { key, keyHash: JSON.stringify(key), state: { value: { status, data } } } as unknown as UseQueryEntry
}

function memoryStorage(initial?: Record<string, string>) {
  const storage = {
    data: new Map<string, string>(Object.entries(initial ?? {})),
    setItem(k: string, v: string) { this.data.set(k, v) },
    removeItem(k: string) { this.data.delete(k) },
    getItem(k: string) { return this.data.get(k) ?? null },
  }
  return storage as unknown as Storage
}

function cacheWith(entries: UseQueryEntry[]) {
  return { getEntries: vi.fn(() => entries) } as unknown as QueryCache
}

const { predicate } = persistableQueryFilter as {
  predicate: (entry: UseQueryEntry) => boolean
}

describe('persistableQueryFilter', () => {
  it('persists every successful query by default, catalogs and providers included', () => {
    const persisted = [
      ['models'],
      ['providers'],
      ['bot', 'qf-2'],
      ['memory-providers'],
      ['speech-providers'],
      ['video-providers'],
      [{ _id: 'getBots', baseUrl: 'http://x' }],
      ['something-new'],
    ]
    for (const key of persisted) {
      expect(predicate(entryWith(key)), `expected ${String(key[0])} to persist`).toBe(true)
    }
  })

  it('excludes whole-payload secrets, volatile state, and opaque workspace files', () => {
    for (const key of [['remote-runtimes'], ['session-status', 'b', 's'], ['bot-hooks-config', 'bot-1']]) {
      expect(predicate(entryWith(key)), `expected ${String(key[0])} to be excluded`).toBe(false)
    }
  })

  it('keeps lists whose edit form overwrites the whole stored config and their field-schema meta off disk', () => {
    for (const key of [['bot-settings', 'bot-1'], ['search-providers'], ['fetch-providers'], ['search-providers-meta'], ['fetch-providers-meta'], ['bot-channels', 'bot-1'], ['provider-templates']]) {
      expect(predicate(entryWith(key)), `expected ${String(key[0])} to be excluded`).toBe(false)
    }
  })

  it('rejects non-success entries', () => {
    expect(predicate(entryWith(['models'], 'pending'))).toBe(false)
    expect(predicate(entryWith(['models'], 'error'))).toBe(false)
  })
})

describe('QUERY_CACHE_STORAGE_KEY', () => {
  it('is the contract auth-session.ts clears on auth changes', () => {
    expect(QUERY_CACHE_STORAGE_KEY).toBe('memoh:query-cache')
  })
})

describe('saveQueryCacheToDiskNow', () => {
  it('persists provider lists and strips secrets recursively', () => {
    const storage = memoryStorage()
    const speechProviders = entryWith(['speech-providers'], 'success', [
      { id: 'p1', name: 'WeakReference', provider: 'native', config: { api_key: 'sk-1', timeout: 30 } },
    ])
    const memoryProviders = entryWith(['memory-providers'], 'success', [
      { id: 'm1', name: 'MemoryProvider', provider: 'builtin', config: { memory_mode: 'graph', token: 't-1' } },
    ])

    saveQueryCacheToDiskNow(cacheWith([speechProviders, memoryProviders]), storage)
    const raw = storage.getItem(QUERY_CACHE_STORAGE_KEY)
    expect(raw).toBeTruthy()
    // Display fields survive…
    expect(raw).toContain('WeakReference')
    expect(raw).toContain('MemoryProvider')
    expect(raw).toContain('memory_mode')
    expect(raw).toContain('timeout')
    // …secrets do not.
    expect(raw).not.toContain('sk-1')
    expect(raw).not.toContain('t-1')
    expect(raw).not.toContain('api_key')
    expect(raw).not.toContain('token')
  })

  it('strips nested bot metadata secrets but keeps display fields', () => {
    const storage = memoryStorage()
    const bot = entryWith(['bot', 'qf-2'], 'success', {
      id: 'bot-uuid',
      name: 'qf-2',
      metadata: { acp: { api_key: 'secret', agent: 'claude' } },
    })

    saveQueryCacheToDiskNow(cacheWith([bot]), storage)
    const raw = storage.getItem(QUERY_CACHE_STORAGE_KEY)
    expect(raw).toContain('bot-uuid')
    expect(raw).toContain('qf-2')
    expect(raw).toContain('agent')
    expect(raw).not.toContain('secret')
    expect(raw).not.toContain('api_key')
  })

  it('never strips look-alike display fields (author, oauth_provider)', () => {
    const storage = memoryStorage()
    const entry = entryWith(['models'], 'success', [
      { id: 'm1', name: 'gpt', author: 'openai', oauth_provider: 'github' },
    ])

    saveQueryCacheToDiskNow(cacheWith([entry]), storage)
    const raw = storage.getItem(QUERY_CACHE_STORAGE_KEY)
    expect(raw).toContain('author')
    expect(raw).toContain('oauth_provider')
  })

  it('strips real-world secret variants (bot_token, appSecret, secret_key, OPENAI_API_KEY, auth_key, setup_key, access_key, app_key)', () => {
    const storage = memoryStorage()
    const entry = entryWith(['channels'], 'success', {
      credentials: {
        bot_token: 'tg-1',
        appSecret: 'wx-1',
        secret_key: 'tx-1',
        auth_key: 'tskey-auth-1',
        setup_key: 'nb-setup-1',
        access_key: 'ak-1',
        app_key: 'appk-1',
      },
      env: { OPENAI_API_KEY: 'sk-1' },
      usage: { token_count: 12, total_tokens: 30 },
      client_id: 'public-id',
      public_key: 'ssh-ed25519-pub',
    })

    saveQueryCacheToDiskNow(cacheWith([entry]), storage)
    const raw = storage.getItem(QUERY_CACHE_STORAGE_KEY)
    for (const leaked of [
      'tg-1', 'wx-1', 'tx-1', 'sk-1', 'tskey-auth-1', 'nb-setup-1', 'ak-1', 'appk-1',
      'bot_token', 'appSecret', 'secret_key', 'OPENAI_API_KEY', 'auth_key', 'setup_key', 'access_key', 'app_key',
    ]) {
      expect(raw).not.toContain(leaked)
    }
    // Token *statistics* and public identifiers are display data, not secrets.
    expect(raw).toContain('token_count')
    expect(raw).toContain('total_tokens')
    expect(raw).toContain('client_id')
    expect(raw).toContain('public_key')
  })

  it('does not persist excluded keys', () => {
    const storage = memoryStorage()
    const entries = [
      entryWith(['remote-runtimes'], 'success', { key: 'runtime-key' }),
      entryWith(['session-status', 'b', 's']),
      entryWith(['bot-settings', 'bot-1'], 'success', { overlay_config: { auth_key: 'tskey-1', setup_key: 'nb-1' } }),
      entryWith(['bot-hooks-config', 'bot-1'], 'success', '{"env":{"OPENAI_API_KEY":"sk-1"}}'),
      entryWith(['search-providers-meta'], 'success', [
        { provider: 'brave', config_schema: { fields: { api_key: { required: true, secret: true } } } },
      ]),
      entryWith(['fetch-providers-meta'], 'success', [
        { provider: 'cloudflare_markdown', config_schema: { fields: { api_token: { required: true, secret: true } } } },
      ]),
    ]

    saveQueryCacheToDiskNow(cacheWith(entries), storage)
    expect(storage.getItem(QUERY_CACHE_STORAGE_KEY)).toBeNull()
  })

  it('swallows a failing write and pauses after the quota is exceeded', () => {
    const storage = memoryStorage()
    const setItem = vi.fn(() => {
      throw new DOMException('quota', 'QuotaExceededError')
    })
    ;(storage as unknown as { setItem: typeof setItem }).setItem = setItem

    expect(() => saveQueryCacheToDiskNow(cacheWith([entryWith(['models'])]), storage)).not.toThrow()
    saveQueryCacheToDiskNow(cacheWith([entryWith(['models'])]), storage)
    expect(setItem).toHaveBeenCalledTimes(1)
    cancelPendingQueryCacheSave()
  })

  it('removes the storage key when nothing qualifies', () => {
    const storage = memoryStorage({ [QUERY_CACHE_STORAGE_KEY]: '{}' })
    saveQueryCacheToDiskNow(cacheWith([entryWith(['remote-runtimes'])]), storage)
    expect(storage.getItem(QUERY_CACHE_STORAGE_KEY)).toBeNull()
  })
})

describe('removeQueryCacheFromDisk', () => {
  it('drops the on-disk copy', () => {
    const storage = memoryStorage({ [QUERY_CACHE_STORAGE_KEY]: '{}' })
    removeQueryCacheFromDisk(storage)
    expect(storage.getItem(QUERY_CACHE_STORAGE_KEY)).toBeNull()
  })
})

describe('cancelPendingQueryCacheSave', () => {
  it('is callable without throwing', () => {
    expect(() => cancelPendingQueryCacheSave()).not.toThrow()
  })
})

describe('context lifecycle queries stay off disk', () => {
  it('excludes the per-turn audit payloads, which grow with conversation length', () => {
    for (const key of [['context-lifecycle', 'b', 's', 50]]) {
      expect(predicate(entryWith(key)), `expected ${String(key[0])} to be excluded`).toBe(false)
    }
  })
})

describe('createQueryCachePersistencePlugin', () => {
  afterEach(() => {
    cancelPendingQueryCacheSave()
    vi.useRealTimers()
  })

  function pluginHarness(setItem: (key: string, value: string) => void) {
    const storage = memoryStorage()
    ;(storage as unknown as { setItem: typeof setItem }).setItem = setItem
    let onAction: ((ctx: { name: string, after: (cb: () => void) => void }) => void) | undefined
    const queryCache = {
      getEntries: vi.fn(() => [entryWith(['models'])]),
      $onAction: vi.fn((cb: typeof onAction) => { onAction = cb }),
    } as unknown as QueryCache
    createQueryCachePersistencePlugin({ storage })({ queryCache } as never)
    return { storage, fire: () => onAction?.({ name: 'setEntryState', after: cb => cb() }) }
  }

  it('recovers the debounce after a transient storage error', async () => {
    vi.useFakeTimers()
    let failNext = true
    const setItem = vi.fn((key: string, value: string) => {
      if (failNext) {
        failNext = false
        throw new DOMException('blocked', 'SecurityError')
      }
      ;(harness.storage as unknown as { data: Map<string, string> }).data.set(key, value)
    })
    const harness = pluginHarness(setItem)
    await Promise.resolve()

    harness.fire()
    vi.advanceTimersByTime(1000)
    expect(setItem).toHaveBeenCalledTimes(1)

    harness.fire()
    vi.advanceTimersByTime(1000)
    expect(setItem).toHaveBeenCalledTimes(2)
    expect(harness.storage.getItem(QUERY_CACHE_STORAGE_KEY)).toContain('models')
  })

  it('stops writing for the rest of the page session once the quota is exceeded', async () => {
    vi.useFakeTimers()
    const setItem = vi.fn(() => {
      throw new DOMException('quota', 'QuotaExceededError')
    })
    const harness = pluginHarness(setItem)
    await Promise.resolve()

    harness.fire()
    vi.advanceTimersByTime(1000)
    harness.fire()
    vi.advanceTimersByTime(1000)
    expect(setItem).toHaveBeenCalledTimes(1)
  })

  it('cancels debounced save timers both before and after remove() so cleared cache never resurrects after 2s', async () => {
    // Failure mode: `createQueryCachePersistencePlugin` subscribes to `$onAction`
    // including `'remove'` and schedules a 1000ms debounced disk write. If
    // `clearQueryCacheAndPersistence` only cancelled timers BEFORE `remove()`,
    // removing entries would re-arm the 1s timer and resurrect `memoh:query-cache`
    // (or overwrite storage) after logout/server-switch.
    vi.useFakeTimers()
    const entries = [entryWith(['models'], 'success', [{ id: 'gpt-5' }]), entryWith(['bot', 'kitty-3'], 'success', { id: 'b1' })]
    const storage = memoryStorage({ [QUERY_CACHE_STORAGE_KEY]: '{"stale":true}' })
    const setItemSpy = vi.spyOn(storage, 'setItem')
    let onAction: ((ctx: { name: string, after: (cb: () => void) => void }) => void) | undefined

    const queryCache = {
      getEntries: vi.fn(() => [...entries]),
      cancelQueries: vi.fn(),
      remove: vi.fn((target: UseQueryEntry) => {
        let afterHook: (() => void) | undefined
        onAction?.({ name: 'remove', after: (cb) => { afterHook = cb } })
        const idx = entries.indexOf(target)
        if (idx >= 0) entries.splice(idx, 1)
        afterHook?.()
      }),
      $onAction: vi.fn((cb: typeof onAction) => { onAction = cb }),
    } as unknown as QueryCache

    createQueryCachePersistencePlugin({ storage })({ queryCache } as never)
    await Promise.resolve()

    // Arm a pre-existing 1s save timer from a recent query update right before logout.
    onAction?.({ name: 'setEntryState', after: cb => cb() })
    setItemSpy.mockClear()

    // Leave one surviving phantom entry in `getEntries()` if `setItem` ever ran again,
    // proving the timer itself never fires rather than merely writing an empty snapshot.
    clearQueryCacheAndPersistence(queryCache, storage)
    entries.push(entryWith(['models'], 'success', [{ id: 'resurrected' }]))

    expect(queryCache.cancelQueries).toHaveBeenCalledTimes(1)
    expect(storage.getItem(QUERY_CACHE_STORAGE_KEY)).toBeNull()

    vi.advanceTimersByTime(2000)
    expect(setItemSpy).not.toHaveBeenCalled()
    expect(storage.getItem(QUERY_CACHE_STORAGE_KEY)).toBeNull()
  })
})

// What can go wrong when a reload restores the on-disk copy:
// 1. The copy was written moments after its fetch, so the restored entry
//    counts as fresh and the page never asks the server again.
// 2. The entry had been invalidated before it was written and comes back fresh.
// 3. A query that never goes stale on its own keeps the restored copy forever.
// 4. Restoring wrongly marks data fetched in this page session as outdated.
// 5. The restored copy is no longer available for the first paint.
describe('restoring the on-disk copy', () => {
  const models = [{ id: 'm1', name: 'Model One' }]

  /** Write `['models']` to disk from a first page session, as the plugin would. */
  function diskWrittenBy(prepare?: (queryCache: QueryCache) => void) {
    const storage = memoryStorage()
    const firstSession = useQueryCache(createPinia())
    firstSession.setQueryData(['models'], models)
    prepare?.(firstSession)
    saveQueryCacheToDiskNow(firstSession, storage)
    return storage
  }

  /** Boot a second page session from `storage`. */
  function reloadFrom(storage: Storage) {
    const queryCache = useQueryCache(createPinia())
    createQueryCachePersistencePlugin({ storage })({ queryCache } as never)
    return queryCache
  }

  afterEach(() => cancelPendingQueryCacheSave())

  it('keeps the restored data for the first paint', () => {
    const queryCache = reloadFrom(diskWrittenBy())
    expect(queryCache.getQueryData(['models'])).toEqual(models)
  })

  it('asks the server again on mount even when the copy was written right after its fetch', async () => {
    const queryCache = reloadFrom(diskWrittenBy())
    const query = vi.fn(async () => models)
    const entry = queryCache.ensure({ key: ['models'], query })

    await queryCache.refresh(entry)
    expect(query).toHaveBeenCalledTimes(1)
  })

  it('asks the server again for an entry that was invalidated before it was written', async () => {
    const storage = diskWrittenBy((firstSession) => {
      for (const entry of firstSession.getEntries({ key: ['models'] })) firstSession.invalidate(entry)
    })
    const queryCache = reloadFrom(storage)
    const query = vi.fn(async () => models)
    const entry = queryCache.ensure({ key: ['models'], query })

    await queryCache.refresh(entry)
    expect(query).toHaveBeenCalledTimes(1)
  })

  it('asks the server again for a query that otherwise never goes stale', async () => {
    const queryCache = reloadFrom(diskWrittenBy())
    const query = vi.fn(async () => models)
    const entry = queryCache.ensure({ key: ['models'], query, staleTime: Number.POSITIVE_INFINITY })

    await queryCache.refresh(entry)
    expect(query).toHaveBeenCalledTimes(1)
  })

  it('leaves data fetched in this page session fresh', async () => {
    const queryCache = reloadFrom(diskWrittenBy())
    queryCache.setQueryData(['bots'], [{ id: 'b1' }])
    const query = vi.fn(async () => [{ id: 'b1' }])
    const entry = queryCache.ensure({ key: ['bots'], query })

    await queryCache.refresh(entry)
    expect(query).not.toHaveBeenCalled()
  })
})
