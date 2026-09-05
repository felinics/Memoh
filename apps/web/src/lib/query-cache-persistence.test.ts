import { afterEach, describe, expect, it, vi } from 'vitest'
import type { QueryCache, UseQueryEntry } from '@pinia/colada'
import {
  QUERY_CACHE_STORAGE_KEY,
  cancelPendingQueryCacheSave,
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
  return {
    data: new Map<string, string>(Object.entries(initial ?? {})),
    setItem(k: string, v: string) { this.data.set(k, v) },
    removeItem(k: string) { this.data.delete(k) },
    getItem(k: string) { return this.data.get(k) ?? null },
  } as unknown as Storage
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
      ['bot-settings', 'bot-1'],
      ['bot', 'qf-2'],
      ['fetch-providers'],
      ['memory-providers'],
      ['search-providers'],
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
    const fetchProviders = entryWith(['fetch-providers'], 'success', [
      { id: 'p1', name: 'WeakReference', provider: 'native', config: { api_key: 'sk-1', timeout: 30 } },
    ])
    const memoryProviders = entryWith(['memory-providers'], 'success', [
      { id: 'm1', name: 'MemoryProvider', provider: 'builtin', config: { memory_mode: 'graph', token: 't-1' } },
    ])

    saveQueryCacheToDiskNow(cacheWith([fetchProviders, memoryProviders]), storage)
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

  it('strips real-world secret variants (bot_token, appSecret, secret_key, OPENAI_API_KEY)', () => {
    const storage = memoryStorage()
    const entry = entryWith(['channels'], 'success', {
      credentials: { bot_token: 'tg-1', appSecret: 'wx-1', secret_key: 'tx-1' },
      env: { OPENAI_API_KEY: 'sk-1' },
      usage: { token_count: 12, total_tokens: 30 },
      client_id: 'public-id',
    })

    saveQueryCacheToDiskNow(cacheWith([entry]), storage)
    const raw = storage.getItem(QUERY_CACHE_STORAGE_KEY)
    for (const leaked of ['tg-1', 'wx-1', 'tx-1', 'sk-1', 'bot_token', 'appSecret', 'secret_key', 'OPENAI_API_KEY']) {
      expect(raw).not.toContain(leaked)
    }
    // Token *statistics* and public identifiers are display data, not secrets.
    expect(raw).toContain('token_count')
    expect(raw).toContain('total_tokens')
    expect(raw).toContain('client_id')
  })

  it('does not persist excluded keys', () => {
    const storage = memoryStorage()
    const entries = [
      entryWith(['remote-runtimes'], 'success', { key: 'runtime-key' }),
      entryWith(['session-status', 'b', 's']),
      entryWith(['bot-hooks-config', 'bot-1'], 'success', '{"env":{"OPENAI_API_KEY":"sk-1"}}'),
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
    for (const key of [
      ['context-lifecycle', 'b', 's', 50],
      ['context-lifecycle-decisions', 'b', 's', 'run'],
      ['context-lifecycle-fragments', 'b', 's', 'run'],
      ['session-compactions', 'b', 's'],
    ]) {
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
})
