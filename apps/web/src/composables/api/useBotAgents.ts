import type { Ref } from 'vue'
import { useQuery } from '@pinia/colada'
import { getBotsByBotIdAgents } from '@memohai/sdk'

/**
 * Query key of a bot's Agent instances. Every page that reads them shares
 * this entry, so a change invalidated under it reaches all of them.
 */
export function botAgentsQueryKey(botId: string): string[] {
  return ['bot-agents', botId]
}

export function useBotAgentsQuery(botId: Ref<string | null | undefined>) {
  return useQuery({
    key: () => botAgentsQueryKey(botId.value ?? ''),
    query: async () => {
      const { data } = await getBotsByBotIdAgents({ path: { bot_id: botId.value! }, throwOnError: true })
      return data
    },
    enabled: () => !!botId.value,
  })
}
