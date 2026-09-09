import { computed, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useQuery } from '@pinia/colada'
import { getBotsByBotIdSessionsBySessionIdContextLifecycle } from '@memohai/sdk'
import type { HandlersContextLifecycleResponse } from '@memohai/sdk'
import { useChatStore } from '@/store/chat-list'
import { useChatViewTarget } from './useChatViewContext'

const PAGE_LIMIT = 50
const MAX_LIMIT = 200

// Mounted only inside the open inspector, so the entry goes inactive with the
// dialog and the short gcTime releases the page instead of pinning it.
export function useContextLifecycle() {
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
    enabled: () => !!botId.value && !!sessionId.value,
    gcTime: 60_000,
    refetchOnWindowFocus: false,
  })

  const hasTarget = computed(() => !!botId.value && !!sessionId.value)
  const canLoadOlder = computed(() => data.value?.has_more === true && limit.value < MAX_LIMIT)
  function loadOlder() {
    limit.value = MAX_LIMIT
  }

  return { data, status, hasTarget, canLoadOlder, loadOlder, maxLimit: MAX_LIMIT }
}
