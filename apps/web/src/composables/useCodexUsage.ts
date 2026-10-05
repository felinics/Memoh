import type { Ref } from 'vue'
import { useQuery } from '@pinia/colada'
import { useDocumentVisibility, useIntervalFn } from '@vueuse/core'
import { getBotsByBotIdAgentsByIdCodexUsage } from '@memohai/sdk'

const POLL_MS = 60_000

// Usage moves with every turn on the account, including turns from other
// clients signed in to the same ChatGPT account, so it is polled while shown.
// The poll goes through refresh() so surfaces sharing the key fetch once per
// stale period instead of once each.
export function useCodexUsage(options: {
  botId: Ref<string>
  botAgentId: Ref<string>
  enabled: () => boolean
}) {
  const visibility = useDocumentVisibility()
  const enabled = () => !!options.botId.value && !!options.botAgentId.value && options.enabled()
  const query = useQuery({
    key: () => ['codex-usage', options.botId.value, options.botAgentId.value],
    enabled,
    staleTime: POLL_MS / 2,
    query: async ({ signal }) => {
      const { data } = await getBotsByBotIdAgentsByIdCodexUsage({
        path: { bot_id: options.botId.value, id: options.botAgentId.value },
        signal,
        throwOnError: true,
      })
      return data
    },
  })
  useIntervalFn(() => {
    if (enabled() && visibility.value === 'visible') void query.refresh()
  }, POLL_MS)
  return query
}
