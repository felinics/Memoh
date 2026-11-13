import { computed, onScopeDispose, toValue, watch, type MaybeRefOrGetter } from 'vue'
import { getActivePinia } from 'pinia'
import { useQuery, useQueryCache, type QueryCache } from '@pinia/colada'
import { getBotsByBotIdContainerFs, getBotsByBotIdContainerFsRead } from '@memohai/sdk'
import type { HandlersFsFileInfo } from '@memohai/sdk'
import { useVisiblePoll } from '@/composables/useVisiblePoll'
import { apiErrorStatus, isApiErrorCode } from '@/utils/api-error'

const DEFAULT_POLL_INTERVAL_MS = 2_000
// Workspace `modTime` is serialized as RFC3339 (1-second resolution). Only lock
// a metadata fingerprint once wall-clock time has moved safely past that whole
// second so consecutive same-second, same-size edits cannot hide behind one stat.
const RFC3339_SETTLE_MARGIN_MS = 1_500

export interface ContainerTextFileSnapshot {
  content: string
  revision: string
  size: number
}

export type StatSample = Pick<HandlersFsFileInfo, 'isDir' | 'size' | 'mode' | 'modTime'>

export function containerTextFileQueryKey(botId: string, path: string) {
  return ['container-fs-text', botId, path] as const
}

export function isContainerFileNotFound(error: unknown): boolean {
  return isApiErrorCode(error, 'workspace.file_not_found') || apiErrorStatus(error) === 404
}

export function utf8ByteLength(text: string): number {
  return new TextEncoder().encode(text).byteLength
}

export function tryUseQueryCache(): QueryCache | null {
  const pinia = getActivePinia()
  if (!pinia || !('_pc_query' in pinia.state.value)) return null
  try {
    return useQueryCache(pinia)
  } catch {
    return null
  }
}

export function syncContainerTextFileCache(
  queryCache: QueryCache | null | undefined,
  botId: string,
  path: string,
  snapshot: ContainerTextFileSnapshot,
) {
  if (!queryCache || !botId || !path) return
  queryCache.setQueryData(containerTextFileQueryKey(botId, path), snapshot)
}

function statFingerprint(stat: StatSample): string {
  return `${stat.size ?? -1}:${stat.mode ?? ''}:${stat.modTime ?? ''}`
}

function isStatSettledBefore(stat: Pick<HandlersFsFileInfo, 'modTime'>, syncAt: number): boolean {
  const modTimeMs = stat.modTime ? Date.parse(stat.modTime) : Number.NaN
  return Number.isFinite(modTimeMs) && modTimeMs < syncAt - RFC3339_SETTLE_MARGIN_MS
}

export function createContainerFileStatTracker() {
  let knownFingerprint: string | null = null
  let lastSyncSize: number | null = null
  let lastSyncAt = 0

  return {
    reset() {
      knownFingerprint = null
      lastSyncSize = null
      lastSyncAt = 0
    },
    recordSync(size: number, syncedAt = Date.now()) {
      knownFingerprint = null
      lastSyncSize = size
      lastSyncAt = syncedAt
    },
    shouldRead(stat: StatSample): boolean {
      if (stat.isDir) return true
      const fp = statFingerprint(stat)
      if (knownFingerprint !== null) return fp !== knownFingerprint
      if (lastSyncSize !== null && stat.size === lastSyncSize && isStatSettledBefore(stat, lastSyncAt)) {
        knownFingerprint = fp
        return false
      }
      return true
    },
    commitRead(stat: StatSample, readSize: number, readAt = Date.now()) {
      lastSyncSize = readSize
      lastSyncAt = readAt
      if (!stat.isDir && stat.size === readSize && isStatSettledBefore(stat, readAt)) {
        knownFingerprint = statFingerprint(stat)
      } else {
        knownFingerprint = null
      }
    },
  }
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
  const botId = computed(() => toValue(botIdSource))
  const path = computed(() => toValue(pathSource))
  const visible = computed(() => Boolean(toValue(options.visible ?? true)))
  const queryEnabled = computed(() => Boolean(toValue(options.enabled ?? true) && visible.value && botId.value && path.value))

  const statTracker = createContainerFileStatTracker()
  let trackedRevision = ''
  let pollController: AbortController | null = null

  const query = useQuery({
    key: () => containerTextFileQueryKey(botId.value, path.value),
    query: async ({ signal }) => {
      const { data } = await getBotsByBotIdContainerFsRead({
        path: { bot_id: botId.value },
        query: { path: path.value },
        signal,
        throwOnError: true,
      })
      const content = data?.content ?? ''
      const revision = data?.revision ?? ''
      const size = data?.size ?? utf8ByteLength(content)
      trackedRevision = revision
      statTracker.recordSync(size, Date.now())
      return { content, revision, size }
    },
    enabled: () => queryEnabled.value,
    gcTime: 60_000,
  })

  watch(
    () => query.data.value,
    (snapshot) => {
      if (snapshot && snapshot.revision !== trackedRevision) {
        trackedRevision = snapshot.revision
        statTracker.recordSync(snapshot.size, Date.now())
      }
    },
  )

  watch([botId, path, queryEnabled], ([, , isEnabled], [prevBot, prevPath]) => {
    if (!isEnabled || botId.value !== prevBot || path.value !== prevPath) {
      pollController?.abort()
      pollController = null
    }
    if (botId.value !== prevBot || path.value !== prevPath) {
      trackedRevision = ''
      statTracker.reset()
    }
  })

  useVisiblePoll(
    async () => {
      const currentBot = botId.value
      const currentPath = path.value
      if (!currentBot || !currentPath || query.isLoading.value || pollController) return
      const controller = new AbortController()
      pollController = controller
      try {
        const { data: stat } = await getBotsByBotIdContainerFs({
          path: { bot_id: currentBot },
          query: { path: currentPath },
          signal: controller.signal,
          throwOnError: true,
        })
        if (controller.signal.aborted || botId.value !== currentBot || path.value !== currentPath) return
        if (!statTracker.shouldRead(stat)) return
        const state = await query.refetch()
        if (!controller.signal.aborted && state.status === 'success' && state.data) {
          statTracker.commitRead(stat, state.data.size, Date.now())
        }
      } catch (error) {
        if (controller.signal.aborted) return
        if (isContainerFileNotFound(error)) {
          statTracker.reset()
          await query.refetch()
        }
      } finally {
        if (pollController === controller) pollController = null
      }
    },
    options.pollIntervalMs ?? DEFAULT_POLL_INTERVAL_MS,
    {
      enabled: () => Boolean(queryEnabled.value && !isContainerFileNotFound(query.error.value)),
    },
  )

  onScopeDispose(() => {
    pollController?.abort()
    pollController = null
  })

  return query
}
