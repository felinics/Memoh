import type { UseQueryEntryFilter } from '@pinia/colada'

/**
 * Cross-reload persistence for the Pinia Colada query cache.
 *
 * The cache is memory-only by default, so a hard refresh cold-starts every
 * useQuery and settings pages render placeholders for at least one RTT
 * (visible against any remote server, and on every desktop app launch).
 * The persister plugin snapshots WHITELISTED entries to localStorage and
 * rehydrates them on boot. Hydrated entries are always older than the
 * default 5s staleTime, so colada revalidates them in the background on
 * mount (SWR) — the snapshot only owes the first paint; correctness always
 * comes from the network.
 *
 * Whitelist, never blacklist: a query is only persisted when named here, so
 * newly added queries default to safe. Only semi-static catalog/config data
 * qualifies — its writes flow through this app's mutations (which re-persist
 * via the plugin), and out-of-band writes (channel slash commands, other
 * devices) converge on the next mount revalidation. Volatile data (sessions,
 * usage, container/status, outbox) stays out: freshness matters more than
 * first paint there, and its invalidation surface is much larger.
 */

/**
 * localStorage key shared by the web app and the desktop renderer. Cleared
 * with the other user-scoped keys on login/logout/token-cleared/unauthorized
 * — see USER_SCOPED_STORAGE_KEYS in lib/auth-session.ts.
 */
export const QUERY_CACHE_STORAGE_KEY = 'memoh:query-cache'

/**
 * First key segment (the string namespace) of every query allowed to
 * persist. Keep in sync with the real call sites: a name no query uses is a
 * harmless no-op, a missing name means that query never persists.
 */
const PERSISTED_QUERY_KEY_HEADS: ReadonlySet<string> = new Set([
  // Global catalogs
  'models',
  'providers',
  'provider-models',
  'provider-templates',
  'provider-template-models',
  'memory-providers',
  'memory-providers-meta',
  'email-providers',
  'email-providers-meta',
  'search-providers',
  'search-providers-meta',
  'fetch-providers',
  'fetch-providers-meta',
  'speech-providers',
  'speech-providers-meta',
  'speech-provider-detail',
  'speech-provider-models',
  'transcription-providers',
  'transcription-providers-meta',
  'transcription-provider-detail',
  'transcription-provider-models',
  'video-providers',
  'video-providers-meta',
  'video-provider-detail',
  'video-provider-models',
  'acp-profiles',
  'channels',
  'connectors-catalog',
  'network-providers-meta',
  'remote-runtimes',
  'platform',
  // Per-bot identity + settings — the Bot Settings cold-start flash reads
  // its model UUID from 'bot-settings' and the display name from 'models'.
  'bot',
  'bot-settings',
])

/**
 * The bots list is the one query keyed by the SDK-generated object form
 * ([{ _id: 'getBots', baseUrl, … }]) instead of a string namespace.
 */
function isBotsListKeyHead(head: unknown): boolean {
  return typeof head === 'object' && head !== null
    && (head as { _id?: unknown })._id === 'getBots'
}

/**
 * Persistence filter for PiniaColadaCachePersister: which entries are
 * written to and restored from the snapshot. Error/pending entries are
 * excluded so a transient failure never gets frozen into the snapshot.
 */
export const queryCachePersistFilter: UseQueryEntryFilter = {
  predicate: (entry) => {
    if (entry.state.value.status !== 'success') return false
    const head = entry.key[0]
    if (typeof head === 'string') return PERSISTED_QUERY_KEY_HEADS.has(head)
    return isBotsListKeyHead(head)
  },
}
