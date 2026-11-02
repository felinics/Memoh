import { toValue, type MaybeRefOrGetter } from 'vue'
import { useQuery } from '@pinia/colada'
import { getBotsByIdChannelByPlatform, getChannels } from '@memohai/sdk'
import type { ChannelChannelConfig, HandlersChannelMeta } from '@memohai/sdk'

export interface BotChannelItem {
  meta: HandlersChannelMeta
  config: ChannelChannelConfig | null
  configured: boolean
}

/**
 * Every configurable platform with this bot's config for it, if any. One query key
 * for the bot's channel list, so the Platforms tab and the places that only list the
 * configured platforms share one fetch and refresh together.
 */
export function useBotChannels(botId: MaybeRefOrGetter<string>) {
  return useQuery({
    key: () => ['bot-channels', toValue(botId)],
    query: async (): Promise<BotChannelItem[]> => {
      const id = toValue(botId)
      const { data: metas } = await getChannels({ throwOnError: true })
      if (!metas) return []
      const configurableTypes = metas.filter((m) => !m.configless)
      return Promise.all(
        configurableTypes.map(async (meta) => {
          try {
            const { data: config } = await getBotsByIdChannelByPlatform({ path: { id, platform: meta.type ?? '' }, throwOnError: true })
            return { meta, config: config ?? null, configured: true }
          } catch {
            return { meta, config: null, configured: false }
          }
        }),
      )
    },
    enabled: () => !!toValue(botId),
  })
}
