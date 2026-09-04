import { computed, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useQuery } from '@pinia/colada'
import { getBotsByBotIdSessionsBySessionIdContextLifecycle } from '@memohai/sdk'
import type { HandlersContextLifecycleResponse } from '@memohai/sdk'
import { useChatStore } from '@/store/chat-list'
import { useChatViewTarget } from './useChatViewContext'
import { mergeLifecyclePages } from './context-lifecycle-view'

const PAGE_LIMIT = 50

// The first page is a query so a finished turn refreshes it; older pages
// follow keyset cursors, which stay valid while newer runs arrive, so they
// are fetched once and kept until the session changes. Mounted only inside
// the open inspector, so the entry goes inactive with the dialog and the
// short gcTime releases the page instead of pinning it.
export function useContextLifecycle() {
  const storeRefs = storeToRefs(useChatStore())
  const viewTarget = useChatViewTarget()
  const botId = computed(() => viewTarget.value.botId || storeRefs.currentBotId.value)
  const sessionId = computed(() => viewTarget.value.sessionId)
  const olderPages = ref<HandlersContextLifecycleResponse[]>([])
  const loadingOlder = ref(false)
  watch(sessionId, () => {
    olderPages.value = []
  }, { flush: 'sync' })

  async function fetchPage(before: string | undefined, signal?: AbortSignal) {
    const { data } = await getBotsByBotIdSessionsBySessionIdContextLifecycle({
      path: { bot_id: botId.value!, session_id: sessionId.value! },
      query: { limit: PAGE_LIMIT, before },
      signal,
      throwOnError: true,
    })
    return data as HandlersContextLifecycleResponse
  }

  const { data, status } = useQuery({
    key: () => ['context-lifecycle', botId.value ?? '', sessionId.value ?? ''],
    query: ({ signal }) => fetchPage(undefined, signal),
    enabled: () => !!botId.value && !!sessionId.value,
    gcTime: 60_000,
    refetchOnWindowFocus: false,
  })

  const hasTarget = computed(() => !!botId.value && !!sessionId.value)
  const merged = computed(() => mergeLifecyclePages(data.value, olderPages.value))
  const turns = computed(() => merged.value.turns)
  const hasOlder = computed(() => merged.value.hasMore || data.value?.legacy_history_may_exist === true)
  const canLoadOlder = computed(() => merged.value.nextCursor != null && !loadingOlder.value)

  async function loadOlder() {
    const cursor = merged.value.nextCursor
    const target = sessionId.value
    if (!cursor || !target || loadingOlder.value) return
    loadingOlder.value = true
    try {
      const page = await fetchPage(cursor)
      if (sessionId.value === target) olderPages.value = [...olderPages.value, page]
    } finally {
      loadingOlder.value = false
    }
  }

  return { data, status, hasTarget, turns, hasOlder, canLoadOlder, loadingOlder, loadOlder }
}
