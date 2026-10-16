import type { Ref } from 'vue'
import { useQuery } from '@pinia/colada'
import { getBotsById, type BotsBot } from '@memohai/sdk'

/**
 * Query key of one bot, addressed by id or name. Every page that reads the
 * bot shares this entry, so they must also share `fetchBot` as the query.
 */
export function botQueryKey(idOrName: string): string[] {
  return ['bot', idOrName]
}

/**
 * Fetches one bot by id or name. A bot that does not exist resolves to null
 * rather than throwing, so callers can tell "no such bot" apart from a failed
 * request; every other failure is thrown.
 */
export async function fetchBot(idOrName: string): Promise<BotsBot | null> {
  const result = await getBotsById({ path: { id: idOrName } })
  if (result.response?.status === 404) return null
  if (result.error !== undefined) throw result.error
  return result.data ?? null
}

export function useBotQuery(idOrName: Ref<string>) {
  return useQuery({
    key: () => botQueryKey(idOrName.value),
    query: () => fetchBot(idOrName.value),
    enabled: () => !!idOrName.value,
  })
}
