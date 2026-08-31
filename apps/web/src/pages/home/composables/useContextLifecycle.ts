import { computed, type Ref } from 'vue'
import { storeToRefs } from 'pinia'
import { useQuery } from '@pinia/colada'
import { getBotsByBotIdSessionsBySessionIdContextLifecycle } from '@memohai/sdk'
import type { HandlersContextLifecycleResponse } from '@memohai/sdk'
import { useChatStore } from '@/store/chat-list'
import { useChatViewTarget } from './useChatViewContext'

const TURN_LIMIT = 50

interface UseContextLifecycleOptions {
  open: Ref<boolean>
  botId?: Ref<string | null | undefined>
  sessionId?: Ref<string | null | undefined>
}

export function useContextLifecycle(options: UseContextLifecycleOptions) {
  const chatStore = useChatStore()
  const storeRefs = storeToRefs(chatStore)
  const viewTarget = useChatViewTarget()
  const botId = options.botId ?? computed(() => viewTarget.value.botId || storeRefs.currentBotId.value)
  const sessionId = options.sessionId ?? computed(() => viewTarget.value.sessionId)

  const { data, status } = useQuery({
    key: () => ['context-lifecycle', botId.value ?? '', sessionId.value ?? ''],
    query: async () => {
      const { data } = await getBotsByBotIdSessionsBySessionIdContextLifecycle({
        path: {
          bot_id: botId.value!,
          session_id: sessionId.value!,
        },
        query: { limit: TURN_LIMIT },
        throwOnError: true,
      })
      return data as HandlersContextLifecycleResponse
    },
    enabled: () => options.open.value && !!botId.value && !!sessionId.value,
    refetchOnWindowFocus: false,
  })

  return { data, status }
}
