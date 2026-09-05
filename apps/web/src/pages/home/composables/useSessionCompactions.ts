import { computed } from 'vue'
import { storeToRefs } from 'pinia'
import { useQuery } from '@pinia/colada'
import { getBotsByBotIdSessionsBySessionIdCompactions } from '@memohai/sdk'
import type { CompactionLog } from '@memohai/sdk'
import { useChatStore } from '@/store/chat-list'
import { useChatViewTarget } from './useChatViewContext'

// The compactions of the session, oldest first, so the trajectory can place
// each one between the turns it ran between. A finished turn refetches them
// together with the lifecycle page.
export function useSessionCompactions() {
  const storeRefs = storeToRefs(useChatStore())
  const viewTarget = useChatViewTarget()
  const botId = computed(() => viewTarget.value.botId || storeRefs.currentBotId.value)
  const sessionId = computed(() => viewTarget.value.sessionId)

  const { data, status } = useQuery({
    key: () => ['session-compactions', botId.value ?? '', sessionId.value ?? ''],
    query: async ({ signal }) => {
      const { data } = await getBotsByBotIdSessionsBySessionIdCompactions({
        path: { bot_id: botId.value!, session_id: sessionId.value! },
        signal,
        throwOnError: true,
      })
      return (data?.items ?? []) as CompactionLog[]
    },
    enabled: () => !!botId.value && !!sessionId.value,
    gcTime: 60_000,
    refetchOnWindowFocus: false,
  })

  const compactions = computed(() => [...(data.value ?? [])]
    .filter(item => item.started_at)
    .sort((a, b) => Date.parse(a.started_at!) - Date.parse(b.started_at!)))
  return { compactions, status }
}
