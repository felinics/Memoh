import { computed, ref, shallowRef, toValue, watch, type MaybeRefOrGetter } from 'vue'
import { useI18n } from 'vue-i18n'
import { useQuery, useQueryCache } from '@pinia/colada'
import {
  getBotsByBotIdStarterPrompts,
  postBotsByBotIdStarterPromptsRefresh,
  type HandlersStarterPrompt,
  type HandlersStarterPromptsResponse,
} from '@memohai/sdk'

export type StarterPrompt = HandlersStarterPrompt

export interface StarterPromptSet {
  /** Hash of the material the set was generated from; empty when there is no set. */
  hash: string
  items: StarterPrompt[]
}

const EMPTY_SET: StarterPromptSet = { hash: '', items: [] }

/**
 * The set to keep after the server answered for the `held` one: the held set
 * when it is unchanged or no new set could be generated, the new set when one
 * came back, and an empty set when there is neither.
 */
export function resolveStarterPromptSet(held: StarterPromptSet | undefined, response: HandlersStarterPromptsResponse): StarterPromptSet {
  if (response.unchanged && held) return held
  if (response.hash && response.items.length > 0) return { hash: response.hash, items: response.items }
  return held ?? EMPTY_SET
}

/**
 * The bot's new-chat suggestions for the signed-in user.
 *
 * The query entry is the cache: Pinia Colada persists it to localStorage and
 * the user store clears it on sign-out. Each mount asks the server whether the
 * held set is still current by sending its hash; the server answers
 * `unchanged`, or generates and returns a new set, which can take a while.
 * Meanwhile the held set stays on screen. A set the server could not generate
 * keeps the held one; with nothing held the result is an empty set, and the
 * caller shows its generic examples.
 *
 * `refresh()` asks for a different set, telling the server which prompts are
 * on screen, and replaces the held set with it. It resolves false when no new
 * set came back, leaving the held one in place.
 */
export function useStarterPromptsQuery(botId: MaybeRefOrGetter<string>) {
  const { locale } = useI18n()
  const queryCache = useQueryCache()
  const key = () => ['starter-prompts', toValue(botId)]

  const query = useQuery({
    key,
    query: async ({ signal }): Promise<StarterPromptSet> => {
      const held = queryCache.getQueryData<StarterPromptSet>(key())
      const { data } = await getBotsByBotIdStarterPrompts({
        path: { bot_id: toValue(botId) },
        query: { locale: locale.value, known_hash: held?.hash || undefined },
        signal,
        throwOnError: true,
      })
      return resolveStarterPromptSet(held, data)
    },
    enabled: () => !!toValue(botId),
    staleTime: 0,
    gcTime: false,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  })

  const refreshing = ref(false)
  const refreshError = shallowRef<unknown>(null)
  watch(() => toValue(botId), () => { refreshError.value = null })
  const error = computed(() => refreshError.value ?? query.error.value)
  async function refresh(): Promise<boolean> {
    const held = queryCache.getQueryData<StarterPromptSet>(key())
    refreshing.value = true
    refreshError.value = null
    try {
      const { data } = await postBotsByBotIdStarterPromptsRefresh({
        path: { bot_id: toValue(botId) },
        body: { locale: locale.value, avoid: held?.items.map(item => item.prompt) ?? [] },
        throwOnError: true,
      })
      if (!data.hash || data.items.length === 0) throw new Error('No starter prompts returned')
      // A read still in flight answered for the set being replaced; drop it.
      queryCache.cancelQueries({ key: key() })
      queryCache.setQueryData<StarterPromptSet>(key(), { hash: data.hash, items: data.items })
      return true
    }
    catch (error) {
      refreshError.value = error
      return false
    }
    finally {
      refreshing.value = false
    }
  }

  return { ...query, error, refresh, refreshing }
}
