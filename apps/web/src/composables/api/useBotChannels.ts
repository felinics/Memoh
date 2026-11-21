import { toValue, type MaybeRefOrGetter } from 'vue'
import { useQuery } from '@pinia/colada'
import { getBotsByIdChannels, getChannels } from '@memohai/sdk'
import type { ChannelChannelConfig, HandlersChannelMeta } from '@memohai/sdk'

export interface BotChannelItem {
  meta: HandlersChannelMeta
  config: ChannelChannelConfig | null
  configured: boolean
}

export interface UseBotChannelsOptions {
  enabled?: MaybeRefOrGetter<boolean>
}

/**
 * Every configurable platform with this bot's config for it, if any. One query key
 * for the bot's channel list, so the Platforms tab and the places that only list the
 * configured platforms share one fetch and refresh together.
 */
export function useBotChannels(botId: MaybeRefOrGetter<string>, options?: UseBotChannelsOptions) {
  return useQuery({
    key: () => ['bot-channels', toValue(botId)],
    query: async (): Promise<BotChannelItem[]> => {
      const id = toValue(botId)
      const [{ data: metas }, { data: batch }] = await Promise.all([
        getChannels({ throwOnError: true }),
        getBotsByIdChannels({ path: { id }, throwOnError: true }),
      ])
      if (!metas) return []
      const configsByType = new Map<string, ChannelChannelConfig>()
      for (const item of batch?.items ?? []) {
        if (item.channel_type) {
          configsByType.set(item.channel_type, item)
        }
      }
      return metas
        .filter((m) => !m.configless)
        .map((meta) => {
          const config = configsByType.get(meta.type ?? '') ?? null
          return { meta, config, configured: config !== null }
        })
    },
    // Persisted localStorage cache strips config.credentials via deepStripSecrets,
    // so always revalidate on mount instead of serving a secret-stripped entry within
    // Colada's default 5s staleTime window.
    staleTime: 0,
    enabled: () => !!toValue(botId) && toValue(options?.enabled ?? true),
  })
}
