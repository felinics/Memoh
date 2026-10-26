import type { Ref } from 'vue'
import { useQuery } from '@pinia/colada'
import { getBotsByBotIdAgentsByIdCodexUsage } from '@memohai/sdk'

// Usage is read from the Agent's Codex, which a read starts when it is not
// running, so it is never polled: callers read it when it can have changed.
export function useCodexUsage(options: {
  botId: Ref<string>
  botAgentId: Ref<string>
  enabled: () => boolean
}) {
  return useQuery({
    key: () => ['codex-usage', options.botId.value, options.botAgentId.value],
    enabled: () => !!options.botId.value && !!options.botAgentId.value && options.enabled(),
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
    query: async ({ signal }) => {
      const { data } = await getBotsByBotIdAgentsByIdCodexUsage({
        path: { bot_id: options.botId.value, id: options.botAgentId.value },
        signal,
        throwOnError: true,
      })
      return data
    },
  })
}
