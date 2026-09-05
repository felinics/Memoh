import { computed, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useQuery } from '@pinia/colada'
import { getBotsByBotIdSessionsBySessionIdCompactions } from '@memohai/sdk'
import type { HandlersSessionCompactionsResponse } from '@memohai/sdk'
import { useChatStore } from '@/store/chat-list'
import { useChatViewTarget } from './useChatViewContext'
import { mergeCompactionPages } from './session-compactions-view'

const PAGE_LIMIT = 50
// A compaction needs many turns, so at most a few can finish between two
// refetches of the first page.
const GAP_LIMIT = 8

// The compactions of the session, oldest first, so the trajectory can place
// each one between the turns it ran between. The first page is a query that
// a finished turn or a manual compaction refetches; older pages follow the
// keyset cursor when the reader loads older history, and stay joined when
// the first page moves on.
export function useSessionCompactions() {
  const storeRefs = storeToRefs(useChatStore())
  const viewTarget = useChatViewTarget()
  const botId = computed(() => viewTarget.value.botId || storeRefs.currentBotId.value)
  const sessionId = computed(() => viewTarget.value.sessionId)
  const olderPages = ref<HandlersSessionCompactionsResponse[]>([])
  const olderAnchor = ref<string | null>(null)
  const loadingOlder = ref(false)
  watch(sessionId, () => {
    olderPages.value = []
    olderAnchor.value = null
  }, { flush: 'sync' })

  async function fetchPage(before: string | undefined, limit = PAGE_LIMIT, signal?: AbortSignal) {
    const { data } = await getBotsByBotIdSessionsBySessionIdCompactions({
      path: { bot_id: botId.value!, session_id: sessionId.value! },
      query: { limit, before },
      signal,
      throwOnError: true,
    })
    return data as HandlersSessionCompactionsResponse
  }

  const { data, status } = useQuery({
    key: () => ['session-compactions', botId.value ?? '', sessionId.value ?? ''],
    query: ({ signal }) => fetchPage(undefined, PAGE_LIMIT, signal),
    enabled: () => !!botId.value && !!sessionId.value,
    gcTime: 60_000,
    refetchOnWindowFocus: false,
  })

  watch(() => data.value?.next_cursor, async (cursor) => {
    if (!cursor || olderPages.value.length === 0 || olderAnchor.value === null || cursor === olderAnchor.value) return
    const target = sessionId.value
    try {
      const gap = await fetchPage(cursor, GAP_LIMIT)
      if (sessionId.value !== target) return
      olderPages.value = [gap, ...olderPages.value]
      olderAnchor.value = cursor
    } catch {
      // The hole stays until the next refetch tries again.
    }
  })

  const merged = computed(() => mergeCompactionPages(data.value, olderPages.value))
  const compactions = computed(() => merged.value.items)
  const canLoadOlder = computed(() => merged.value.nextCursor != null && !loadingOlder.value)

  async function loadOlder() {
    const cursor = merged.value.nextCursor
    const target = sessionId.value
    if (!cursor || !target || loadingOlder.value) return
    loadingOlder.value = true
    try {
      const page = await fetchPage(cursor)
      if (sessionId.value !== target) return
      if (olderPages.value.length === 0) olderAnchor.value = data.value?.next_cursor ?? null
      olderPages.value = [...olderPages.value, page]
    } finally {
      loadingOlder.value = false
    }
  }

  return { compactions, status, canLoadOlder, loadingOlder, loadOlder }
}
