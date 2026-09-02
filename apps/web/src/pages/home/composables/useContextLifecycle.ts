import { computed, ref, watch, type Ref } from 'vue'
import { storeToRefs } from 'pinia'
import { useQuery } from '@pinia/colada'
import {
  getBotsByBotIdSessionsBySessionIdContextLifecycle,
  getBotsByBotIdSessionsBySessionIdContextLifecycleByRunId,
} from '@memohai/sdk'
import type { HandlersContextLifecycleResponse, HandlersContextLifecycleTurn } from '@memohai/sdk'
import { useChatStore } from '@/store/chat-list'
import { useChatViewTarget } from './useChatViewContext'

const PAGE_LIMIT = 50
const MAX_LIMIT = 200

function useLifecycleTarget() {
  const storeRefs = storeToRefs(useChatStore())
  const viewTarget = useChatViewTarget()
  return {
    botId: computed(() => viewTarget.value.botId || storeRefs.currentBotId.value),
    sessionId: computed(() => viewTarget.value.sessionId),
  }
}

export function useContextLifecycle(open: Ref<boolean>) {
  const { botId, sessionId } = useLifecycleTarget()
  const limit = ref(PAGE_LIMIT)
  watch(sessionId, () => {
    limit.value = PAGE_LIMIT
  }, { flush: 'sync' })

  const { data, status } = useQuery({
    key: () => ['context-lifecycle', botId.value ?? '', sessionId.value ?? '', limit.value],
    query: async () => {
      const { data } = await getBotsByBotIdSessionsBySessionIdContextLifecycle({
        path: { bot_id: botId.value!, session_id: sessionId.value! },
        query: { limit: limit.value },
        throwOnError: true,
      })
      return data as HandlersContextLifecycleResponse
    },
    enabled: () => open.value && !!botId.value && !!sessionId.value,
    refetchOnWindowFocus: false,
  })

  const canLoadOlder = computed(() => data.value?.has_more === true && limit.value < MAX_LIMIT)
  function loadOlder() {
    limit.value = MAX_LIMIT
  }

  return { data, status, canLoadOlder, loadOlder }
}

// A persisted run's snapshot never changes, so a fetched turn stays fresh for
// the whole dialog session instead of re-downloading on every re-expand.
export function useContextLifecycleTurn(open: Ref<boolean>, runId: Ref<string | null>) {
  const { botId, sessionId } = useLifecycleTarget()

  const { data, status } = useQuery({
    key: () => ['context-lifecycle-turn', botId.value ?? '', sessionId.value ?? '', runId.value ?? ''],
    query: async () => {
      const { data } = await getBotsByBotIdSessionsBySessionIdContextLifecycleByRunId({
        path: { bot_id: botId.value!, session_id: sessionId.value!, run_id: runId.value! },
        throwOnError: true,
      })
      return data as HandlersContextLifecycleTurn
    },
    enabled: () => open.value && !!runId.value && !!botId.value && !!sessionId.value,
    staleTime: 10 * 60_000,
    refetchOnWindowFocus: false,
  })

  return { data, status }
}
