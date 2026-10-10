import { toValue, type MaybeRefOrGetter } from 'vue'
import { useQuery } from '@pinia/colada'
import { getBotsByBotIdContainer, getBotsByBotIdSettings } from '@memohai/sdk'
import { BUILTIN_CHAT_EXAMPLES } from '@/lib/chat-examples/catalog'
import { capabilitySnapshotFrom, type CapabilityProbes } from '@/lib/chat-examples/capabilities'

/**
 * Usage examples for every surface. Today this resolves the built-in catalog;
 * switching to a backend source only changes the query function here (keep
 * BUILTIN_CHAT_EXAMPLES as the fallback when the request fails).
 */
export function useChatExamplesQuery() {
  return useQuery({
    key: ['chat-examples'],
    query: async () => BUILTIN_CHAT_EXAMPLES,
    staleTime: Number.POSITIVE_INFINITY,
  })
}

/**
 * What the bot can currently do, for marking examples it cannot run yet.
 * Probes run in parallel and a failed probe (e.g. 403 for chat-only members)
 * leaves its capabilities unknown rather than failing the query.
 */
export function useBotCapabilitiesQuery(botId: MaybeRefOrGetter<string>) {
  return useQuery({
    key: () => ['bot-capabilities', toValue(botId)],
    query: async () => {
      const path = { bot_id: toValue(botId) }
      const [settings, container] = await Promise.allSettled([
        getBotsByBotIdSettings({ path, throwOnError: true }),
        getBotsByBotIdContainer({ path }),
      ])
      const probes: CapabilityProbes = {}
      if (settings.status === 'fulfilled') probes.settings = settings.value.data ?? null
      if (container.status === 'fulfilled') {
        const { data, response } = container.value
        if (data) probes.container = data
        else if (response?.status === 404) probes.container = null
      }
      return capabilitySnapshotFrom(probes)
    },
    enabled: () => !!toValue(botId),
    staleTime: 60_000,
  })
}
