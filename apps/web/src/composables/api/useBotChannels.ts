import { toValue, type MaybeRefOrGetter } from 'vue'
import { useQuery } from '@pinia/colada'
import { getBotsByIdChannels, getBotsByIdChannelByPlatform, getChannels } from '@memohai/sdk'
import type { ChannelChannelConfig, HandlersChannelMeta } from '@memohai/sdk'
import { apiErrorStatus, isApiErrorCode } from '@/utils/api-error'

export interface BotChannelItem {
  meta: HandlersChannelMeta
  config: ChannelChannelConfig | null
  configured: boolean
}

export interface UseBotChannelsOptions {
  enabled?: MaybeRefOrGetter<boolean>
}

interface BatchLookupResult {
  supported: boolean
  configsByType: Map<string, ChannelChannelConfig>
}

function isMissingEndpointFallback(error: unknown): boolean {
  // Distinguish an older server without `GET /bots/:id/channels` (generic 404)
  // from a deleted/unknown bot (`bot.not_found`).
  return apiErrorStatus(error) === 404 && !isApiErrorCode(error, 'bot.not_found')
}

async function fetchConfiguredChannelsBatch(botId: string): Promise<BatchLookupResult> {
  try {
    const { data } = await getBotsByIdChannels({ path: { id: botId }, throwOnError: true })
    if (!data || typeof data !== 'object' || !Array.isArray((data as { items?: unknown }).items)) {
      // Older all-in-one servers serve index.html (200 text/html) for unknown
      // backend paths instead of JSON 404.
      return { supported: false, configsByType: new Map() }
    }
    const configsByType = new Map<string, ChannelChannelConfig>()
    for (const item of data.items) {
      if (item.channel_type) {
        configsByType.set(item.channel_type, item)
      }
    }
    return { supported: true, configsByType }
  } catch (error) {
    if (isMissingEndpointFallback(error)) {
      return { supported: false, configsByType: new Map() }
    }
    throw error
  }
}

async function fetchConfiguredChannelsLegacy(
  botId: string,
  configurableTypes: HandlersChannelMeta[],
): Promise<BotChannelItem[]> {
  return Promise.all(
    configurableTypes.map(async (meta) => {
      try {
        const { data: config } = await getBotsByIdChannelByPlatform({
          path: { id: botId, platform: meta.type ?? '' },
          throwOnError: true,
        })
        return { meta, config: config ?? null, configured: true }
      } catch (error) {
        if (isApiErrorCode(error, 'channel.config_not_found') || apiErrorStatus(error) === 404) {
          return { meta, config: null, configured: false }
        }
        throw error
      }
    }),
  )
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
      const [{ data: metas }, batch] = await Promise.all([
        getChannels({ throwOnError: true }),
        fetchConfiguredChannelsBatch(id),
      ])
      if (!metas) return []
      const configurableTypes = metas.filter((m) => !m.configless)
      if (!batch.supported) {
        return fetchConfiguredChannelsLegacy(id, configurableTypes)
      }
      return configurableTypes.map((meta) => {
        const config = batch.configsByType.get(meta.type ?? '') ?? null
        return { meta, config, configured: config !== null }
      })
    },
    // Persisted localStorage cache strips config.credentials via deepStripSecrets,
    // so always revalidate on mount instead of serving a secret-stripped entry within
    // Colada's default 5s staleTime window.
    staleTime: 0,
    enabled: () => !!toValue(botId) && (options?.enabled === undefined ? true : toValue(options.enabled)),
  })
}
