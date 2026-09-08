import { computed, onScopeDispose, shallowRef, watch, type Ref } from 'vue'
import { useQuery } from '@pinia/colada'
import { useIntervalFn } from '@vueuse/core'
import { getBotsByBotIdSessionsBySessionIdContextTrajectory } from '@memohai/sdk'
import { useChatViewTarget } from './useChatViewContext'
import { mergeContextCapturePages } from './context-trajectory-view'
import type { ContextCapturePage } from './context-trajectory.types'

export function useContextTrajectory(active: Ref<boolean>) {
  const target = useChatViewTarget()
  const scope = computed(() => `${target.value.botId}/${target.value.sessionId}`)
  const pages = shallowRef<ContextCapturePage[]>([])
  const loadingOlder = shallowRef(false)
  const fillingGap = shallowRef(false)
  const loadError = shallowRef<unknown>(null)
  const merged = computed(() => mergeContextCapturePages(pages.value))
  let generation = 0
  let controller: AbortController | undefined

  watch(scope, () => {
    generation += 1
    controller?.abort()
    pages.value = []
    loadingOlder.value = false
    fillingGap.value = false
    loadError.value = null
  }, { flush: 'sync' })
  onScopeDispose(() => { generation += 1; controller?.abort() })

  async function fetchPage(before?: string, signal?: AbortSignal) {
    const { botId, sessionId } = target.value
    const requestedScope = scope.value
    const { data } = await getBotsByBotIdSessionsBySessionIdContextTrajectory({
      path: { bot_id: botId!, session_id: sessionId! }, query: { limit: 200, before }, signal, throwOnError: true,
    })
    if (!data) throw new Error('Missing context trajectory response')
    return { scope: requestedScope, page: { before, data } }
  }

  function addPage(page: ContextCapturePage) {
    const first = page.data.events?.[0]?.id
    pages.value = [page, ...pages.value.filter(existing => existing.before !== page.before || existing.data.events?.[0]?.id !== first)]
  }

  const query = useQuery({
    key: () => ['context-trajectory', target.value.botId ?? '', target.value.sessionId ?? ''],
    query: ({ signal }) => fetchPage(undefined, signal),
    enabled: () => active.value && !!target.value.botId && !!target.value.sessionId,
    gcTime: 60_000,
    refetchOnWindowFocus: false,
  })

  async function loadOlder() {
    const before = merged.value.gapCursor ?? merged.value.nextCursor
    if (!before || loadingOlder.value) return
    const expected = generation
    controller = new AbortController()
    loadingOlder.value = true
    loadError.value = null
    try {
      const result = await fetchPage(before, controller.signal)
      if (expected === generation && result.scope === scope.value) addPage(result.page)
    } catch (error) {
      if (expected === generation) loadError.value = error
    } finally {
      if (expected === generation) loadingOlder.value = false
    }
  }

  async function fillGap() {
    if (fillingGap.value || loadingOlder.value) return
    const expected = generation
    fillingGap.value = true
    try {
      for (let count = 0; count < 6 && expected === generation && active.value && merged.value.gapCursor; count += 1) {
        await loadOlder()
        if (loadError.value) break
      }
    } finally {
      if (expected === generation) fillingGap.value = false
    }
  }

  watch(query.data, (value) => {
    if (!value || value.scope !== scope.value) return
    addPage(value.page)
    void fillGap()
  }, { immediate: true })

  async function refresh() {
    loadError.value = null
    await query.refetch()
    await fillGap()
  }

  const polling = useIntervalFn(() => {
    if (query.asyncStatus.value !== 'loading' && !loadingOlder.value) void refresh()
  }, 2000, { immediate: active.value })
  watch(active, (enabled) => {
    if (enabled) { polling.resume(); void refresh() }
    else polling.pause()
  })

  return {
    events: computed(() => merged.value.events),
    status: query.status,
    error: computed(() => loadError.value ?? query.error.value ?? null),
    hasGap: computed(() => merged.value.gapCursor != null),
    invalidEntries: computed(() => merged.value.invalidEntries),
    canLoadOlder: computed(() => merged.value.gapCursor != null || merged.value.nextCursor != null),
    loadingOlder,
    loadOlder,
    refresh,
  }
}
