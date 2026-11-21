import { computed, toValue, type MaybeRefOrGetter } from 'vue'
import { useQuery } from '@pinia/colada'
import { getBotsByBotIdSettings } from '@memohai/sdk'

/**
 * The timezone a bot's schedules fire in, as the server resolves it: the
 * bot's own timezone, then its owner's, then the system default. Schedule
 * views compute next runs in this zone, never in the browser's, because the
 * viewer may be in a different zone from the bot.
 *
 * Shares the `['bot-settings', botId]` entry with every other reader of the
 * bot's settings. `timezone` stays undefined until the settings load.
 */
export function useBotEffectiveTimezone(botId: MaybeRefOrGetter<string | null | undefined>) {
  const { data, isPending } = useQuery({
    key: () => ['bot-settings', toValue(botId) ?? ''],
    query: async () => {
      const { data } = await getBotsByBotIdSettings({ path: { bot_id: toValue(botId)! }, throwOnError: true })
      return data
    },
    enabled: () => !!toValue(botId),
  })
  const timezone = computed(() => data.value?.effective_timezone?.trim() || undefined)
  return { timezone, isPending }
}
