import { computed, onScopeDispose, toValue, watch, type MaybeRefOrGetter } from 'vue'
import { useQueryCache } from '@pinia/colada'
import { getBotsQueryKey } from '@memohai/sdk/colada'
import type { BotsBot } from '@memohai/sdk'

const POLL_INTERVAL_MS = 2000

/**
 * Keeps the shared bots list query fresh while any bot in `bots` is
 * `creating` or `deleting`. The server finishes both transitions in the
 * background, so without polling the cached list freezes at the pending
 * state — e.g. a deleted bot lingers in the switcher long after the server
 * removed it. Each caller polls only while its own scope is alive; Colada
 * collapses overlapping refetches of the same key.
 */
export function usePendingBotsRefresh(bots: MaybeRefOrGetter<BotsBot[]>) {
  const queryCache = useQueryCache()
  const hasPendingBots = computed(() =>
    toValue(bots).some(bot => bot.status === 'creating' || bot.status === 'deleting'),
  )

  let pollTimer: ReturnType<typeof setInterval> | null = null

  function stop() {
    if (pollTimer == null) return
    clearInterval(pollTimer)
    pollTimer = null
  }

  watch(hasPendingBots, (pending) => {
    if (!pending) {
      stop()
      return
    }
    pollTimer ??= setInterval(() => {
      void queryCache.invalidateQueries({ key: getBotsQueryKey() })
    }, POLL_INTERVAL_MS)
  }, { immediate: true })

  onScopeDispose(stop)
}
