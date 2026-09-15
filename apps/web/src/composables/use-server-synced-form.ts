import { readonly, ref, watch, type Ref } from 'vue'

/**
 * Why this exists: @pinia/colada refetches active queries when the window
 * regains visibility (its default `refetchOnWindowFocus`) and hands back
 * FRESH object identities every time. A settings form that watches props or
 * query data and blindly re-assigns its editable state therefore wipes
 * in-flight edits whenever the window comes back — dock switch on desktop,
 * tab switch / occlusion on web.
 *
 * These composables own the reconciliation between the two writers of the
 * same form — the user's draft and the server snapshot — using the contract
 * first written down in provider-form.vue ("a refetch landing mid-edit must
 * not clobber it"):
 *
 *  - identity change (user switched bot / provider / ...) → HARD RESET:
 *    server values replace everything, in-flight edits included;
 *  - same-identity background refresh → PER-FIELD GUARD: only fields still
 *    equal to the last-known-server snapshot track the server; fields the
 *    user has diverged from the snapshot keep their in-flight value.
 *
 * Do not "fix" this at the query layer instead: disabling
 * refetchOnWindowFocus starves read surfaces that legitimately want fresh
 * data, and structural sharing still wipes drafts whenever server data
 * genuinely changes.
 */

interface ServerSyncedBaseOptions<S> {
  /** Watched source — typically a query's data or a prop derived from it. */
  source: () => S
  /** Subject identity; a change means the user switched subjects → hard reset. */
  identity: (source: S) => string
  /** Forwarded to Vue's watch `deep` option (`immediate` is always on). */
  deep?: boolean
}

export interface UseServerSyncedRecordOptions<S> extends ServerSyncedBaseOptions<S> {
  /** Derive the server-side field values from the watched source. */
  server: (source: S) => Record<string, unknown>
  /** Runs after each reconciliation. */
  onSync?: (hardReset: boolean, synced: Record<string, unknown>) => void
}

/**
 * Keeps a reactive record (e.g. a credentials/config form) in sync with
 * server state without ever clobbering in-flight edits. See file header for
 * the contract.
 */
export function useServerSyncedRecord<S>(
  form: Record<string, unknown>,
  options: UseServerSyncedRecordOptions<S>,
) {
  let lastIdentity: string | null = null
  const synced = ref<Record<string, unknown>>({})

  watch(options.source, (source) => {
    const server = options.server(source)
    const identity = options.identity(source)
    const hardReset = identity !== lastIdentity
    lastIdentity = identity
    const snapshot = hardReset ? {} : synced.value
    const keys = new Set([...Object.keys(server), ...Object.keys(snapshot), ...Object.keys(form)])
    for (const key of keys) {
      if (!hardReset && JSON.stringify(form[key]) !== JSON.stringify(snapshot[key])) continue
      if (key in server) form[key] = server[key]
      else delete form[key]
    }
    // Defensive copy: the derivation may share nested references with the
    // watched source, and the snapshot must stay a pure baseline.
    synced.value = { ...server }
    options.onSync?.(hardReset, synced.value)
  }, { immediate: true, deep: options.deep })

  /**
   * Call after a successful save: until the refetch lands, the form's current
   * values ARE the new server state, so they become the guard baseline.
   */
  function markClean() {
    synced.value = { ...form }
  }

  return { synced: readonly(synced), markClean }
}

export interface UseServerSyncedScalarOptions<S, T> extends ServerSyncedBaseOptions<S> {
  /** Derive the server-side value from the watched source. */
  server: (source: S) => T
  /** Runs after each reconciliation. */
  onSync?: (hardReset: boolean) => void
}

/**
 * Same contract as useServerSyncedRecord for a single ref field (a name, a
 * select value, ...). A diverged value survives background refreshes; an
 * untouched value tracks the server.
 */
export function useServerSyncedScalar<S, T>(
  target: Ref<T>,
  options: UseServerSyncedScalarOptions<S, T>,
) {
  let lastIdentity: string | null = null
  const synced = ref<T>() as Ref<T | undefined>

  watch(options.source, (source) => {
    const serverValue = options.server(source)
    const identity = options.identity(source)
    const hardReset = identity !== lastIdentity
    lastIdentity = identity
    if (hardReset || JSON.stringify(target.value) === JSON.stringify(synced.value)) {
      target.value = serverValue
    }
    synced.value = serverValue
    options.onSync?.(hardReset)
  }, { immediate: true, deep: options.deep })

  /** See useServerSyncedRecord.markClean. */
  function markClean() {
    synced.value = target.value
  }

  return { synced: readonly(synced), markClean }
}
