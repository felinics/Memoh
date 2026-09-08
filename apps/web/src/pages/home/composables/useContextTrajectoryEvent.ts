import { computed, type Ref } from 'vue'
import { useQuery } from '@pinia/colada'
import { getBotsByBotIdSessionsBySessionIdContextTrajectoryByEventId } from '@memohai/sdk'
import { useChatViewTarget } from './useChatViewContext'

export function useContextTrajectoryEvent(eventId: Ref<string | null | undefined>) {
  const target = useChatViewTarget()
  const scope = computed(() => `${target.value.botId}/${target.value.sessionId}/${eventId.value ?? ''}`)
  const query = useQuery({
    key: () => ['context-trajectory-event', target.value.botId ?? '', target.value.sessionId ?? '', eventId.value ?? ''],
    enabled: () => !!target.value.botId && !!target.value.sessionId && !!eventId.value,
    query: async ({ signal }) => {
      const requestedScope = scope.value
      const { data } = await getBotsByBotIdSessionsBySessionIdContextTrajectoryByEventId({
        path: { bot_id: target.value.botId!, session_id: target.value.sessionId!, event_id: eventId.value! },
        signal, throwOnError: true,
      })
      if (!data?.event || !Array.isArray(data.blocks)) throw new Error('Missing context trajectory content')
      return { scope: requestedScope, data }
    },
    gcTime: 60_000,
    refetchOnWindowFocus: false,
  })
  return {
    data: computed(() => query.data.value?.scope === scope.value ? query.data.value.data : undefined),
    status: query.status, error: query.error, refresh: query.refetch,
  }
}
