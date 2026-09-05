import { computed, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useQuery } from '@pinia/colada'
import { useI18n } from 'vue-i18n'
import { toast } from '@felinic/ui'
import { getBotsByBotIdSessionsBySessionIdContextLifecycle } from '@memohai/sdk'
import type { HandlersContextLifecycleResponse } from '@memohai/sdk'
import { useChatStore } from '@/store/chat-list'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { useChatViewTarget } from './useChatViewContext'
import { compactLifecyclePages, lifecycleGapBefore, lifecycleGapJoins, mergeLifecyclePages } from './context-lifecycle-view'

const PAGE_LIMIT = 50
// A finished turn moves the first page on by one run; the fill covers a few
// turns finishing between refetches, and follows its cursor while it has
// not reached the loaded older window, up to a bound.
const GAP_LIMIT = 8
const MAX_GAP_PAGES = 6

// The first page is a query so a finished turn refreshes it; older pages
// follow keyset cursors below the first page's cursor at the time they were
// fetched. When the first page moves on, the small gap between it and the
// older window is filled rather than the older window dropped. The query is
// mounted by the trajectory panel, and the short gcTime releases the page
// once it is gone.
export function useContextLifecycle() {
  const { t } = useI18n()
  const storeRefs = storeToRefs(useChatStore())
  const viewTarget = useChatViewTarget()
  const botId = computed(() => viewTarget.value.botId || storeRefs.currentBotId.value)
  const sessionId = computed(() => viewTarget.value.sessionId)
  const olderPages = ref<HandlersContextLifecycleResponse[]>([])
  const olderAnchor = ref<string | null>(null)
  const loadingOlder = ref(false)
  const fillingGap = ref(false)
  watch(sessionId, () => {
    olderPages.value = []
    olderAnchor.value = null
  }, { flush: 'sync' })

  async function fetchPage(before: string | undefined, limit = PAGE_LIMIT, signal?: AbortSignal) {
    const { data } = await getBotsByBotIdSessionsBySessionIdContextLifecycle({
      path: { bot_id: botId.value!, session_id: sessionId.value! },
      query: { limit, before },
      signal,
      throwOnError: true,
    })
    return data as HandlersContextLifecycleResponse
  }

  const { data, status } = useQuery({
    key: () => ['context-lifecycle', botId.value ?? '', sessionId.value ?? ''],
    query: ({ signal }) => fetchPage(undefined, PAGE_LIMIT, signal),
    enabled: () => !!botId.value && !!sessionId.value,
    gcTime: 60_000,
    refetchOnWindowFocus: false,
  })

  watch(() => data.value?.next_cursor, async (cursor) => {
    const before = lifecycleGapBefore(cursor, olderAnchor.value, olderPages.value.length > 0)
    if (!before || fillingGap.value) return
    fillingGap.value = true
    const target = sessionId.value
    try {
      const pages: HandlersContextLifecycleResponse[] = []
      let cursor: string | undefined = before
      for (let count = 0; cursor && count < MAX_GAP_PAGES; count += 1) {
        const page: HandlersContextLifecycleResponse = await fetchPage(cursor, GAP_LIMIT)
        if (sessionId.value !== target) return
        pages.push(page)
        if (lifecycleGapJoins(page, olderPages.value)) break
        cursor = page.next_cursor
      }
      const last = pages[pages.length - 1]
      if (last && !lifecycleGapJoins(last, olderPages.value)) {
        // Too many runs finished to bridge: the older window no longer
        // joins, so it is released rather than shown with a hole.
        olderPages.value = []
        olderAnchor.value = null
        return
      }
      olderPages.value = [compactLifecyclePages([...pages, ...olderPages.value])]
      olderAnchor.value = before
    } catch {
      // The hole stays until the next finished turn tries again.
    } finally {
      fillingGap.value = false
    }
  })

  const hasTarget = computed(() => !!botId.value && !!sessionId.value)
  const merged = computed(() => mergeLifecyclePages(data.value, olderPages.value))
  const turns = computed(() => merged.value.turns)
  const fragmentPreviews = computed(() => merged.value.fragmentPreviews)
  const hasOlder = computed(() => merged.value.hasMore || data.value?.legacy_history_may_exist === true)
  const canLoadOlder = computed(() => merged.value.nextCursor != null && !loadingOlder.value)

  async function loadOlder() {
    const cursor = merged.value.nextCursor
    const target = sessionId.value
    const anchor = data.value?.next_cursor ?? null
    if (!cursor || !target || loadingOlder.value) return
    loadingOlder.value = true
    try {
      const page = await fetchPage(cursor)
      if (sessionId.value !== target) return
      if (olderPages.value.length === 0) olderAnchor.value = anchor
      olderPages.value = [compactLifecyclePages([...olderPages.value, page])]
    } catch (error) {
      toast.error(resolveApiErrorMessage(error, t('chat.lifecycle.loadFailed')))
    } finally {
      loadingOlder.value = false
    }
  }

  return { data, status, hasTarget, turns, fragmentPreviews, hasOlder, canLoadOlder, loadingOlder, loadOlder }
}
