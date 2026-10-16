import { computed, ref, toValue, watch, type MaybeRefOrGetter } from 'vue'
import { storeToRefs } from 'pinia'
import { useQuery } from '@pinia/colada'
import { getBotsByBotIdSessionsBySessionIdContextLifecycle } from '@memohai/sdk'
import type { HandlersContextLifecycleResponse } from '@memohai/sdk'
import { useChatStore } from '@/store/chat-list'
import { useChatViewTarget } from './useChatViewContext'

const PAGE_LIMIT = 50
const MAX_LIMIT = 200

// The inspector stays mounted once opened, so `open` gates the query: while
// the dialog is closed, turn-end invalidations skip the refetch and the short
// gcTime releases the page instead of pinning it.
export function useContextLifecycle(open: MaybeRefOrGetter<boolean>) {
  const storeRefs = storeToRefs(useChatStore())
  const viewTarget = useChatViewTarget()
  const botId = computed(() => viewTarget.value.botId || storeRefs.currentBotId.value)
  const sessionId = computed(() => viewTarget.value.sessionId)
  const limit = ref(PAGE_LIMIT)
  watch(sessionId, () => {
    limit.value = PAGE_LIMIT
  }, { flush: 'sync' })

  const { data, status } = useQuery({
    key: () => ['context-lifecycle', botId.value ?? '', sessionId.value ?? '', limit.value],
    query: async ({ signal }) => {
      const { data } = await getBotsByBotIdSessionsBySessionIdContextLifecycle({
        path: { bot_id: botId.value!, session_id: sessionId.value! },
        query: { limit: limit.value },
        signal,
        throwOnError: true,
      })
      return data as HandlersContextLifecycleResponse
    },
    enabled: () => toValue(open) && !!botId.value && !!sessionId.value,
    gcTime: 60_000,
    refetchOnWindowFocus: false,
  })

  const hasTarget = computed(() => !!botId.value && !!sessionId.value)
  const hasOlder = computed(() => data.value?.has_more === true || data.value?.legacy_history_may_exist === true)
  const canLoadOlder = computed(() => data.value?.has_more === true && limit.value < MAX_LIMIT)
  function loadOlder() {
    limit.value = MAX_LIMIT
  }

  return { data, status, hasTarget, hasOlder, canLoadOlder, loadOlder, maxLimit: MAX_LIMIT }
}
