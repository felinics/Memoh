import { computed, type Ref } from 'vue'
import { storeToRefs } from 'pinia'
import { useQuery } from '@pinia/colada'
import { getBotsByBotIdSessionsBySessionIdContextLifecycleByRunIdDecisions } from '@memohai/sdk'
import type { ContextfragSelectionDecision } from '@memohai/sdk'
import { useChatStore } from '@/store/chat-list'
import { useChatViewTarget } from './useChatViewContext'

// The per-fragment audit of one run is fetched only while an inspector shows
// it: it grows with the history the run considered, so it never rides the
// lifecycle page.
export function useContextLifecycleDecisions(runId: Ref<string | null>) {
  const storeRefs = storeToRefs(useChatStore())
  const viewTarget = useChatViewTarget()
  const botId = computed(() => viewTarget.value.botId || storeRefs.currentBotId.value)
  const sessionId = computed(() => viewTarget.value.sessionId)

  const { data, status } = useQuery({
    key: () => ['context-lifecycle-decisions', botId.value ?? '', sessionId.value ?? '', runId.value ?? ''],
    query: async ({ signal }) => {
      const { data } = await getBotsByBotIdSessionsBySessionIdContextLifecycleByRunIdDecisions({
        path: { bot_id: botId.value!, session_id: sessionId.value!, run_id: runId.value! },
        signal,
        throwOnError: true,
      })
      return (data?.decisions ?? []) as ContextfragSelectionDecision[]
    },
    enabled: () => !!botId.value && !!sessionId.value && !!runId.value,
    gcTime: 60_000,
    refetchOnWindowFocus: false,
  })

  return { decisions: computed(() => data.value ?? []), status }
}
