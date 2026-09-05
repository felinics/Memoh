import {
  _queryEntry_toJSON as queryEntryToJSON,
  hydrateQueryCache,
  type PiniaColadaPlugin,
  type QueryCache,
  type UseQueryEntryFilter,
} from '@pinia/colada'

/**
 * Persist Pinia Colada query results to localStorage across reloads (#936 / #950).
 *
 * Three layers to keep in mind:
 * 1. In-memory query cache — live API results inside the SPA session.
 * 2. localStorage (`memoh:query-cache`) — display-safe copy that survives Cmd+R.
 * 3. Server — source of truth; Colada revalidates stale entries in the background.
 *
 * Every successful query persists by default; on write, `deepStripSecrets`
 * recursively removes secret-bearing fields (api_key, token, …) so provider
 * configs and bot metadata never reach disk, while display fields (id, name,
 * provider, memory_mode, …) survive and hydrate the first paint. Only keys on
 * EXCLUDED_QUERY_KEY_HEADS (entire-payload credentials, volatile state,
 * opaque workspace file reads such as bot-hooks-config) skip disk.
 *
 * After a successful save in-app, call `saveQueryCacheToDiskNow()` so a quick
 * reload does not show a pre-save value (see bot-settings autosave).
 */

export const QUERY_CACHE_STORAGE_KEY = 'memoh:query-cache'

const SAVE_TO_DISK_DEBOUNCE_MS = 1000

// A full origin will not drain within the page session, so one quota failure
// stops the whole-cache serialization instead of retrying it on every change.
let quotaExceeded = false

function isQuotaExceeded(error: unknown): boolean {
  return error instanceof DOMException
    && (error.name === 'QuotaExceededError' || error.name === 'NS_ERROR_DOM_QUOTA_REACHED' || error.code === 22 || error.code === 1014)
}

/**
 * Keys that never touch disk: either the whole payload is a credential
 * (remote-runtimes) or the data is volatile session state, not display data.
 */
const EXCLUDED_QUERY_KEY_HEADS: ReadonlySet<string> = new Set([
  'remote-runtimes',
  'session-status',
  // The trajectory's per-run reads: the audit grows with the history a run
  // considered, and the texts are workspace files and hook output that must
  // not rest on disk.
  'context-lifecycle-decisions',
  'context-lifecycle-fragments',
  'session-compactions',
  // Computer ACL state (which bot may use which runtime). Volatile and
  // cross-surface: writes land via the access dialog / bot page / API, so a
  // hydrated copy can silently disagree with the server — always refetch.
  'bot-workspace-targets',
  'computer-access-grants',
  // Raw workspace file text (hooks.json env map may contain API keys); deepStripSecrets
  // only walks objects/arrays and cannot redact secrets inside an opaque string.
  'bot-hooks-config',
  // Per-turn context audit: a debug surface with no reload value.
  'context-lifecycle',
])

/**
 * Suffix/segment match on the normalized (snake_case, lowercase) key, so
 * real-world variants — bot_token, app_secret, secret_key, OPENAI_API_KEY,
 * appSecret — all strip, while display fields like `author`, `oauth_provider`,
 * `token_count`, `total_tokens`, `public_key` survive untouched.
 */
const SECRET_FIELD_PATTERN = /(^|_)(api_?key|apikey|token|password|passwd|private_?key|credentials?|authorization|auth_?token|bearer)$|^secret(_|$)|_secret(_|$)/

function isSecretField(key: string): boolean {
  const normalized = key.replace(/([a-z0-9])([A-Z])/g, '$1_$2').toLowerCase()
  return SECRET_FIELD_PATTERN.test(normalized)
}

function deepStripSecrets(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(deepStripSecrets)
  if (value && typeof value === 'object') {
    return Object.fromEntries(
      Object.entries(value as Record<string, unknown>)
        .filter(([key]) => !isSecretField(key))
        .map(([key, val]) => [key, deepStripSecrets(val)]),
    )
  }
  return value
}

export const persistableQueryFilter: UseQueryEntryFilter = {
  predicate: (entry) => {
    if (entry.state.value.status !== 'success') return false
    const head = entry.key[0]
    // SDK object keys ({ _id: 'getBots', … }) persist like everything else;
    // their secrets are stripped on write, not filtered here.
    if (typeof head !== 'string') return true
    return !EXCLUDED_QUERY_KEY_HEADS.has(head)
  },
}

function serializeEntryForDisk(entry: { key: readonly unknown[]; keyHash: string; state: { value: { status: string } } }): [string, unknown] {
  const json = queryEntryToJSON(entry as Parameters<typeof queryEntryToJSON>[0]) as unknown[]
  if (json.length === 0) return [entry.keyHash, json]
  return [entry.keyHash, [deepStripSecrets(json[0]), ...json.slice(1)]]
}

function serializePersistableEntries(queryCache: QueryCache): Record<string, unknown> {
  const entries = queryCache.getEntries({ status: 'success' })
  const { predicate } = persistableQueryFilter as { predicate?: (entry: (typeof entries)[number]) => boolean }
  const persistable = predicate ? entries.filter(predicate) : entries
  return Object.fromEntries(
    persistable.map((entry) => serializeEntryForDisk(entry)),
  )
}

/**
 * Write the in-memory cache (secrets stripped) to localStorage immediately,
 * skipping debounce. Persistence is best effort: a failed write keeps the old
 * on-disk copy, and a full origin pauses writing for the rest of the page
 * session instead of retrying on every change.
 */
export function saveQueryCacheToDiskNow(
  queryCache: QueryCache,
  storage: Storage | undefined = typeof localStorage !== 'undefined' ? localStorage : undefined,
) {
  if (!storage || quotaExceeded) return
  try {
    const serialized = serializePersistableEntries(queryCache)
    if (Object.keys(serialized).length === 0) {
      storage.removeItem(QUERY_CACHE_STORAGE_KEY)
      return
    }
    storage.setItem(QUERY_CACHE_STORAGE_KEY, JSON.stringify(serialized))
  } catch (error) {
    if (isQuotaExceeded(error)) {
      quotaExceeded = true
      console.warn('[query-cache] storage quota exceeded; persistence paused for this page session')
    } else {
      console.warn('[query-cache] save to disk failed', error)
    }
  }
}

/** Remove the on-disk copy; in-memory query cache is untouched. */
export function removeQueryCacheFromDisk(
  storage: Storage | undefined = typeof localStorage !== 'undefined' ? localStorage : undefined,
) {
  storage?.removeItem(QUERY_CACHE_STORAGE_KEY)
}

let restoreResolve: (() => void) | undefined
let restorePromise: Promise<void> = new Promise((resolve) => {
  restoreResolve = resolve
})

/** Resolves after localStorage has been read into the in-memory query cache. */
export function whenQueryCacheRestored(): Promise<void> {
  return restorePromise
}

/** @internal Vitest-only reset for whenQueryCacheRestored(). */
export function resetQueryCacheRestoreForTests() {
  restorePromise = new Promise((resolve) => {
    restoreResolve = resolve
  })
}

let saveTimer: ReturnType<typeof setTimeout> | undefined
let saveAgainAfterCurrent = false
let activeStorage: Storage | undefined
let activeQueryCache: QueryCache | undefined

function scheduleDebouncedSaveToDisk() {
  if (!activeStorage || !activeQueryCache || quotaExceeded) return
  if (saveTimer) {
    saveAgainAfterCurrent = true
    return
  }
  saveTimer = setTimeout(() => {
    try {
      saveQueryCacheToDiskNow(activeQueryCache!, activeStorage)
    } finally {
      saveTimer = undefined
    }
    if (saveAgainAfterCurrent) {
      saveAgainAfterCurrent = false
      scheduleDebouncedSaveToDisk()
    }
  }, SAVE_TO_DISK_DEBOUNCE_MS)
}

/** Cancel a pending debounced save (call before logout clears disk). */
export function cancelPendingQueryCacheSave() {
  if (saveTimer) {
    clearTimeout(saveTimer)
    saveTimer = undefined
  }
  saveAgainAfterCurrent = false
  quotaExceeded = false
}

/**
 * Pinia Colada plugin: restore from localStorage on boot, debounce-save on change.
 * Inlined from @pinia/colada-plugin-cache-persister so we can flush/cancel on demand.
 */
export function createQueryCachePersistencePlugin(options?: {
  key?: string
  storage?: Storage
  debounce?: number
}): PiniaColadaPlugin {
  const key = options?.key ?? QUERY_CACHE_STORAGE_KEY
  const storage = options?.storage ?? (typeof localStorage !== 'undefined' ? localStorage : undefined)
  void options?.debounce // fixed at SAVE_TO_DISK_DEBOUNCE_MS; kept for API compat

  return ({ queryCache }) => {
    activeStorage = storage
    activeQueryCache = queryCache

    async function restoreFromDisk() {
      if (!storage) {
        restoreResolve?.()
        return
      }
      try {
        const stored = storage.getItem(key)
        if (stored) hydrateQueryCache(queryCache, JSON.parse(stored))
      } catch {
        // Corrupt on-disk copy — cold start.
      }
      restoreResolve?.()
    }

    void restoreFromDisk()

    queryCache.$onAction(({ name, after }) => {
      if (name === 'setEntryState' || name === 'setQueryData' || name === 'remove') {
        after(() => scheduleDebouncedSaveToDisk())
      }
    })
  }
}
