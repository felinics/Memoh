import { computed, onScopeDispose, shallowRef, toValue, watch, type MaybeRefOrGetter } from 'vue'
import { useQuery, useQueryCache, type QueryCache } from '@pinia/colada'
import { getBotsByBotIdContainerFs, getBotsByBotIdContainerFsRead } from '@memohai/sdk'
import type { HandlersFsFileInfo } from '@memohai/sdk'
import { useVisiblePoll } from '@/composables/useVisiblePoll'
import { apiErrorStatus, isApiErrorCode } from '@/utils/api-error'

const DEFAULT_POLL_INTERVAL_MS = 2_000
// Workspace `modTime` is serialized as RFC3339 with 1-second resolution, so two
// same-size writes inside one second produce identical stats. A fingerprint is
// only trusted once it has been observed unchanged for longer than that second
// (plus margin for request jitter), measured on the browser's own monotonic
// clock so the result does not depend on browser and workspace clocks agreeing.
const FINGERPRINT_SETTLE_MS = 1_500
// Shortcut for files that were last modified long before the content was read:
// comparing the workspace `modTime` with the browser clock is only safe with a
// tolerance that covers clock skew and the duration of the read itself. When
// the clocks disagree by more than this, the shortcut simply does not apply and
// the monotonic rule above decides.
const CLOCK_SKEW_TOLERANCE_MS = 60_000

export interface ContainerTextFileSnapshot {
  content: string
  revision: string
  size: number
}

export type StatSample = Pick<HandlersFsFileInfo, 'isDir' | 'size' | 'mode' | 'modTime'>

export type StatDecision = 'skip' | 'read' | 'read-and-lock'

export function containerTextFileQueryKey(botId: string, path: string) {
  return ['container-fs-text', botId, path] as const
}

export function isContainerFileNotFound(error: unknown): boolean {
  return isApiErrorCode(error, 'workspace.file_not_found') || apiErrorStatus(error) === 404
}

export function utf8ByteLength(text: string): number {
  return new TextEncoder().encode(text).byteLength
}

function statFingerprint(stat: StatSample): string {
  return `${stat.size ?? -1}:${stat.mode ?? ''}:${stat.modTime ?? ''}`
}

// Decides, from lightweight stats alone, when the file body has to be read
// again. "Locked" means the content the consumers hold is known to match the
// fingerprint AND the fingerprint's modTime second had already ended when that
// content was read, so any later write must change the fingerprint.
export function createContainerFileStatTracker() {
  let lockedFingerprint: string | null = null
  let candidateFingerprint: string | null = null
  let candidateSince = 0
  // Set when content arrives outside a stat-driven read (initial load, save,
  // explicit refetch). The consumers are up to date as of that moment, so the
  // next read waits until the fingerprint has settled instead of firing twice.
  let contentFresh = false
  let lastSync: { size: number, wallAt: number } | null = null

  return {
    reset() {
      lockedFingerprint = null
      candidateFingerprint = null
      candidateSince = 0
      contentFresh = false
      lastSync = null
    },
    // `ownWrite` marks content the browser itself just wrote (a save): the
    // fingerprint is about to change because of that write, so the next stat
    // starts a new candidate instead of looking like an external change.
    recordSync(size: number, wallAt = Date.now(), ownWrite = false) {
      lockedFingerprint = null
      contentFresh = true
      lastSync = { size, wallAt }
      if (ownWrite) candidateFingerprint = null
    },
    observe(stat: StatSample, now = performance.now()): StatDecision {
      if (stat.isDir) return 'read'
      const fingerprint = statFingerprint(stat)
      if (fingerprint === lockedFingerprint) return 'skip'
      if (fingerprint !== candidateFingerprint) {
        // Two stats disagree: the file was written between them, so content
        // obtained before this stat can no longer be assumed current.
        if (candidateFingerprint !== null) contentFresh = false
        candidateFingerprint = fingerprint
        candidateSince = now
      }
      // Unchanged for longer than the modTime resolution: that second is over
      // in the workspace, so a read issued now can be locked to this stat.
      if (now - candidateSince >= FINGERPRINT_SETTLE_MS) return 'read-and-lock'
      if (lastSync && stat.size === lastSync.size) {
        const modTimeMs = stat.modTime ? Date.parse(stat.modTime) : Number.NaN
        if (modTimeMs < lastSync.wallAt - FINGERPRINT_SETTLE_MS - CLOCK_SKEW_TOLERANCE_MS) {
          lockedFingerprint = fingerprint
          contentFresh = false
          return 'skip'
        }
      }
      if (contentFresh) return 'skip'
      // The file changed since it was last checked: read once right away so
      // the change shows up promptly, then again once the fingerprint settles.
      contentFresh = true
      return 'read'
    },
    lock(stat: StatSample) {
      lockedFingerprint = statFingerprint(stat)
      contentFresh = false
    },
  }
}

// One tracker and one poll in flight per file, shared by every mounted
// consumer (editor and preview of the same file side by side), so an external
// change costs one read no matter how many panels show the file.
interface SharedFileWatch {
  tracker: ReturnType<typeof createContainerFileStatTracker>
  consumers: number
  polling: boolean
  lastPollAt: number
  // Bumped whenever content is written into the cache from outside the query
  // (see syncContainerTextFileCache). A read that was already in flight at
  // that point may carry older bytes and must not replace the synced content.
  syncEpoch: number
}

const sharedWatches = new Map<string, SharedFileWatch>()

function sharedWatchKey(botId: string, path: string): string {
  return `${botId}\u0000${path}`
}

// Publishes content a consumer obtained on its own (the editor's load and
// save) to every other consumer of the same file.
export function syncContainerTextFileCache(
  queryCache: QueryCache,
  botId: string,
  path: string,
  snapshot: ContainerTextFileSnapshot,
) {
  if (!botId || !path) return
  const shared = sharedWatches.get(sharedWatchKey(botId, path))
  if (shared) {
    shared.syncEpoch += 1
    shared.tracker.recordSync(snapshot.size, Date.now(), true)
  }
  queryCache.setQueryData(containerTextFileQueryKey(botId, path), snapshot)
}

export function useContainerTextFileQuery(
  botIdSource: MaybeRefOrGetter<string>,
  pathSource: MaybeRefOrGetter<string>,
  options: {
    visible?: MaybeRefOrGetter<boolean>
    enabled?: MaybeRefOrGetter<boolean>
    pollIntervalMs?: number
  } = {},
) {
  const queryCache = useQueryCache()
  const botId = computed(() => toValue(botIdSource))
  const path = computed(() => toValue(pathSource))
  const visible = computed(() => Boolean(toValue(options.visible ?? true)))
  const queryEnabled = computed(() => Boolean(toValue(options.enabled ?? true) && visible.value && botId.value && path.value))
  const pollIntervalMs = options.pollIntervalMs ?? DEFAULT_POLL_INTERVAL_MS

  let shared: SharedFileWatch | null = null
  let sharedKey = ''
  let pollController: AbortController | null = null
  // A stat that failed for a reason other than "not found". The body may still
  // be readable, so this is reported separately from the query's own error.
  const pollError = shallowRef<unknown>(null)

  function releaseShared() {
    if (!shared) return
    shared.consumers -= 1
    if (shared.consumers <= 0) sharedWatches.delete(sharedKey)
    shared = null
    sharedKey = ''
  }

  watch([botId, path], ([nextBot, nextPath]) => {
    pollController?.abort()
    pollController = null
    pollError.value = null
    releaseShared()
    if (!nextBot || !nextPath) return
    sharedKey = sharedWatchKey(nextBot, nextPath)
    let next = sharedWatches.get(sharedKey)
    if (!next) {
      next = { tracker: createContainerFileStatTracker(), consumers: 0, polling: false, lastPollAt: Number.NEGATIVE_INFINITY, syncEpoch: 0 }
      sharedWatches.set(sharedKey, next)
    }
    next.consumers += 1
    shared = next
  }, { immediate: true })

  watch(queryEnabled, (isEnabled) => {
    if (isEnabled) return
    pollController?.abort()
    pollController = null
  })

  const query = useQuery({
    key: () => containerTextFileQueryKey(botId.value, path.value),
    query: async ({ signal }) => {
      const requestBot = botId.value
      const requestPath = path.value
      const requestWatch = sharedWatches.get(sharedWatchKey(requestBot, requestPath))
      const epochAtStart = requestWatch?.syncEpoch
      const { data } = await getBotsByBotIdContainerFsRead({
        path: { bot_id: requestBot },
        query: { path: requestPath },
        signal,
        throwOnError: true,
      })
      if (requestWatch && requestWatch.syncEpoch !== epochAtStart) {
        const synced = queryCache.getQueryData<ContainerTextFileSnapshot>(containerTextFileQueryKey(requestBot, requestPath))
        if (synced) return synced
      }
      const content = data?.content ?? ''
      const size = data?.size ?? utf8ByteLength(content)
      requestWatch?.tracker.recordSync(size)
      return { content, revision: data?.revision ?? '', size }
    },
    enabled: () => queryEnabled.value,
    // Freshness is decided by the stat poll below, not by age: becoming visible
    // again must cost one stat, not an unconditional re-read of the body.
    staleTime: Number.POSITIVE_INFINITY,
    gcTime: 60_000,
  })

  useVisiblePoll(
    async () => {
      const watchAtStart = shared
      // No content yet: the query's first read is about to start or already
      // under way, and a stat now would only trigger a second one.
      if (!watchAtStart || query.isPending.value || query.isLoading.value || watchAtStart.polling) return
      const startedAt = performance.now()
      // Another consumer of the same file checked it within this interval.
      // Consumers mount at different moments, so their timers are offset; the
      // margin below one interval absorbs timer jitter of a single consumer.
      if (startedAt - watchAtStart.lastPollAt < pollIntervalMs * 0.75) return
      watchAtStart.polling = true
      watchAtStart.lastPollAt = startedAt
      const controller = new AbortController()
      pollController = controller
      const superseded = () => controller.signal.aborted || shared !== watchAtStart
      const epochBeforeStat = watchAtStart.syncEpoch
      try {
        const { data: stat } = await getBotsByBotIdContainerFs({
          path: { bot_id: botId.value },
          query: { path: path.value },
          signal: controller.signal,
          throwOnError: true,
        })
        if (superseded()) return
        pollError.value = null
        // Content was synced while this stat was in flight; the stat may
        // describe the file from before that sync.
        if (watchAtStart.syncEpoch !== epochBeforeStat) return
        const decision = watchAtStart.tracker.observe(stat)
        if (decision === 'skip') return
        const epochBeforeRead = watchAtStart.syncEpoch
        const state = await query.refetch()
        if (superseded() || state.status !== 'success') return
        if (decision === 'read-and-lock' && watchAtStart.syncEpoch === epochBeforeRead) {
          watchAtStart.tracker.lock(stat)
        }
      } catch (error) {
        if (superseded()) return
        if (!isContainerFileNotFound(error)) {
          pollError.value = error
          return
        }
        watchAtStart.tracker.reset()
        // Surface the deletion through the query once; later polls keep
        // statting so a recreated file is picked up again.
        if (!isContainerFileNotFound(query.error.value)) await query.refetch()
      } finally {
        watchAtStart.polling = false
        if (pollController === controller) pollController = null
      }
    },
    pollIntervalMs,
    {
      enabled: queryEnabled,
      immediate: true,
    },
  )

  onScopeDispose(() => {
    pollController?.abort()
    pollController = null
    releaseShared()
  })

  return { ...query, pollError }
}
