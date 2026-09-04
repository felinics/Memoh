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
import { mergeLifecyclePages } from './context-lifecycle-view'

const PAGE_LIMIT = 50

// The first page is a query so a finished turn refreshes it; older pages
// follow keyset cursors below the first page's cursor at the time they were
// fetched, so they join the list only while that cursor still holds. The
// query is mounted by the inspector dialog and the trajectory panel, and the
// short gcTime releases the page once both are gone.
export function useContextLifecycle() {
  const { t } = useI18n()
  const storeRefs = storeToRefs(useChatStore())
  const viewTarget = useChatViewTarget()
  const botId = computed(() => viewTarget.value.botId || storeRefs.currentBotId.value)
  const sessionId = computed(() => viewTarget.value.sessionId)
  const olderPages = ref<HandlersContextLifecycleResponse[]>([])
  const olderAnchor = ref<string | null>(null)
  const loadingOlder = ref(false)
  watch(sessionId, () => {
    olderPages.value = []
    olderAnchor.value = null
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
  const merged = computed(() => mergeLifecyclePages(data.value, olderPages.value, olderAnchor.value))
  const turns = computed(() => merged.value.turns)
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
      if (olderAnchor.value !== anchor) {
        olderPages.value = [page]
        olderAnchor.value = anchor
      } else {
        olderPages.value = [...olderPages.value, page]
      }
    } catch (error) {
      toast.error(resolveApiErrorMessage(error, t('chat.lifecycle.loadFailed')))
    } finally {
      loadingOlder.value = false
    }
  }

  return { data, status, hasTarget, turns, hasOlder, canLoadOlder, loadingOlder, loadOlder }
}
