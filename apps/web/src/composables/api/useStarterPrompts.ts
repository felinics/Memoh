import { ref, toValue, type MaybeRefOrGetter } from 'vue'
import { useI18n } from 'vue-i18n'
import { useQuery, useQueryCache } from '@pinia/colada'
import {
  getBotsByBotIdStarterPrompts,
  postBotsByBotIdStarterPromptsRefresh,
  type HandlersStarterPrompt,
  type HandlersStarterPromptsResponse,
} from '@memohai/sdk'

export type StarterPrompt = HandlersStarterPrompt

/** Why no set can be generated until someone changes a setting. */
export type StarterPromptsReason = NonNullable<HandlersStarterPromptsResponse['reason']>

/** Root of every starter prompts query key; invalidate it when what generates them changes. */
export const STARTER_PROMPTS_QUERY_KEY = 'starter-prompts'

export interface StarterPromptSet {
  /** Hash of the material the set was generated from; empty when there is no set. */
  hash: string
  items: StarterPrompt[]
  /** Set only on an empty set, when nothing can be generated until a setting changes. */
  reason?: StarterPromptsReason
}

const EMPTY_SET: StarterPromptSet = { hash: '', items: [] }

/**
 * The set to keep after the server answered for the `held` one: the held set
 * when it is unchanged or no new set could be generated, the new set when one
 * came back, and an empty set when there is neither. A held set with items
 * outlives a `reason` (its suggestions still work); otherwise the empty set
 * carries the reason, and only for as long as the server keeps reporting it.
 */
export function resolveStarterPromptSet(held: StarterPromptSet | undefined, response: HandlersStarterPromptsResponse): StarterPromptSet {
  if (response.unchanged && held) return held
  if (response.hash && response.items.length > 0) return { hash: response.hash, items: response.items }
  if (held?.items.length) return held
  return response.reason ? { ...EMPTY_SET, reason: response.reason } : EMPTY_SET
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
 * set came back, leaving the held one in place, unless the server says why:
 * then the held set gives way to the empty set carrying that reason.
 */
export function useStarterPromptsQuery(botId: MaybeRefOrGetter<string>) {
  const { locale } = useI18n()
  const queryCache = useQueryCache()
  const key = () => [STARTER_PROMPTS_QUERY_KEY, toValue(botId)]

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
  async function refresh(): Promise<boolean> {
    const held = queryCache.getQueryData<StarterPromptSet>(key())
    refreshing.value = true
    try {
      const { data } = await postBotsByBotIdStarterPromptsRefresh({
        path: { bot_id: toValue(botId) },
        body: { locale: locale.value, avoid: held?.items.map(item => item.prompt) ?? [] },
        throwOnError: true,
      })
      const next: StarterPromptSet | null = data.hash && data.items.length > 0
        ? { hash: data.hash, items: data.items }
        : data.reason ? { ...EMPTY_SET, reason: data.reason } : null
      if (!next) return false
      // A read still in flight answered for the set being replaced; drop it.
      queryCache.cancelQueries({ key: key() })
      queryCache.setQueryData<StarterPromptSet>(key(), next)
      return true
    }
    catch {
      return false
    }
    finally {
      refreshing.value = false
    }
  }

  return { ...query, refresh, refreshing }
}
