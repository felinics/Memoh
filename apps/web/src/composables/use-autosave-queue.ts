import { watch, type WatchStopHandle } from 'vue'

// Serialized autosave queue for settings-style forms (canonical user:
// bot-settings.vue). The interaction contract comes from the web skill §8:
// a page of toggles/selects auto-saves, success stays silent, only errors
// surface (toast) with a guarded rollback.
//
// Why serialized: the backend applies partial updates as read-merge-write on
// the whole bots row without a transaction, so concurrent field saves can
// lose each other's updates. One in-flight pass at a time; edits that land
// during a pass queue exactly one follow-up pass.
//
// Why snapshot-diff: cards mutate the shared `form` reactive directly (props
// mutation), so a single deep watch + diff against `synced` (last-known
// server state) is the only save trigger — the cards need no save plumbing.
// Vue batches same-tick mutations into one watch fire, which is what groups
// linked writes (e.g. ACP agent + runtime + project fields) into one job,
// matching the backend's merged-state validation.

export interface AutosaveJob<T extends Record<string, unknown>> {
  // Fields this job persists. On success they advance `synced`; on failure
  // they roll back to `synced` — unless the user edited them after the job
  // was built (a newer edit owns the control) or `rollback` is false.
  payload: Partial<T>
  save: () => Promise<void>
  // Set false for draft-style fields whose inline state already carries the
  // error (e.g. the URL name: a 409 must leave the draft text for the user
  // to fix, not snap it back).
  rollback?: boolean
  onSaved?: () => void
  onError?: (error: unknown) => void
}

export interface AutosaveQueueOptions<T extends Record<string, unknown>> {
  form: T
  synced: T
  // Page-owned partition of changed keys into per-endpoint jobs — card
  // boundaries ≠ API boundaries (this page's timezone lives on a different
  // endpoint than the rest of the card). Return [] when nothing is saveable
  // yet (e.g. a name whose availability check is still pending).
  buildJobs: (changed: (keyof T)[]) => AutosaveJob<T>[]
  // Runs once when a queue run fully drains with the keys saved during the
  // run — the single place to invalidate queries, so invalidations are
  // batched per drain instead of per request.
  onDrained?: (savedKeys: Set<keyof T>) => void
}

export function useAutosaveQueue<T extends Record<string, unknown>>(options: AutosaveQueueOptions<T>) {
  const { form, synced, buildJobs, onDrained } = options
  let syncing = false
  let queued = false

  function scheduleSync() {
    if (syncing) {
      queued = true
      return
    }
    void run()
  }

  const stop: WatchStopHandle = watch(form, scheduleSync)

  async function run() {
    if (syncing) return
    syncing = true
    const savedKeys = new Set<keyof T>()
    try {
      for (;;) {
        queued = false
        const changed = (Object.keys(form) as (keyof T)[]).filter((key) => form[key] !== synced[key])
        if (changed.length === 0) break
        const jobs = buildJobs(changed)
        if (jobs.length === 0) break
        let failed = false
        for (const job of jobs) {
          const keys = Object.keys(job.payload) as (keyof T)[]
          try {
            await job.save()
            for (const key of keys) {
              synced[key] = job.payload[key] as T[keyof T]
              savedKeys.add(key)
            }
            job.onSaved?.()
          } catch (error) {
            job.onError?.(error)
            if (job.rollback !== false) {
              for (const key of keys) {
                if (form[key] === job.payload[key]) form[key] = synced[key]
              }
            }
            // No retry: the next user edit re-schedules. Retrying here could
            // hot-loop on a persistent server error.
            failed = true
          }
        }
        if (failed) break
      }
    } finally {
      syncing = false
      if (savedKeys.size > 0) onDrained?.(savedKeys)
      // A change landed between the last diff and the lock release.
      if (queued) scheduleSync()
    }
  }

  return { scheduleSync, stop }
}
