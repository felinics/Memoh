import { computed, type Ref } from 'vue'
import { storeToRefs } from 'pinia'
import { useQuery } from '@pinia/colada'
import { getBotsByBotIdSessionsBySessionIdContextLifecycleByRunIdFragments } from '@memohai/sdk'
import type { HandlersContextFragmentText } from '@memohai/sdk'
import { useChatStore } from '@/store/chat-list'
import { useChatViewTarget } from './useChatViewContext'

// The full texts of a run's injected fragments are read only while an
// inspector shows them; the ledger itself works from the page's previews.
export function useContextLifecycleFragments(runId: Ref<string | null>) {
  const storeRefs = storeToRefs(useChatStore())
  const viewTarget = useChatViewTarget()
  const botId = computed(() => viewTarget.value.botId || storeRefs.currentBotId.value)
  const sessionId = computed(() => viewTarget.value.sessionId)

  const { data, status, error } = useQuery({
    key: () => ['context-lifecycle-fragments', botId.value ?? '', sessionId.value ?? '', runId.value ?? ''],
    query: async ({ signal }) => {
      const { data } = await getBotsByBotIdSessionsBySessionIdContextLifecycleByRunIdFragments({
        path: { bot_id: botId.value!, session_id: sessionId.value!, run_id: runId.value! },
        signal,
        throwOnError: true,
      })
      return (data?.fragments ?? []) as HandlersContextFragmentText[]
    },
    enabled: () => !!botId.value && !!sessionId.value && !!runId.value,
    gcTime: 60_000,
    refetchOnWindowFocus: false,
  })

  return { fragments: computed(() => data.value ?? []), status, error }
}
