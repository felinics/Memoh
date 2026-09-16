import { onActivated, onBeforeUnmount, onDeactivated, onMounted, watch } from 'vue'

const ACTIVE_POLL_MS = 3_000
const IDLE_POLL_MS = 15_000

/**
 * A ready dependency can start recovering outside this page, so foreground
 * observation must continue after the last known operation reaches a terminal
 * state. The callback only reads Apps and current workspace discovery.
 */
export function useAppStatusRefresh(options: {
  botId: () => string
  detailKey: () => string
  inProgress: () => boolean
  refresh: () => Promise<unknown>
}) {
  let active = false
  let disposed = false
  let refreshing = false
  let contextRevision = 0
  let timer: ReturnType<typeof setTimeout> | undefined

  function stopTimer() {
    clearTimeout(timer)
    timer = undefined
  }

  function canRefresh() {
    return !disposed && active && !!options.botId() && document.visibilityState !== 'hidden'
  }

  function schedule() {
    stopTimer()
    if (canRefresh() && !refreshing) {
      timer = setTimeout(() => { void refresh() }, options.inProgress() ? ACTIVE_POLL_MS : IDLE_POLL_MS)
    }
  }

  async function refresh() {
    stopTimer()
    if (!canRefresh() || refreshing) return
    refreshing = true
    const revision = contextRevision
    try {
      await options.refresh()
    } catch {
      // The query owns error presentation. A transient read failure must not
      // stop observation of a recovery that continues on the server.
    } finally {
      refreshing = false
      if (revision !== contextRevision && canRefresh()) void refresh()
      else schedule()
    }
  }

  function onVisible() {
    if (canRefresh()) void refresh()
    else stopTimer()
  }

  function activate() {
    if (active) return
    active = true
    void refresh()
  }

  watch([options.botId, options.detailKey], () => {
    contextRevision++
    void refresh()
  })
  watch(options.inProgress, schedule)

  onMounted(() => {
    document.addEventListener('visibilitychange', onVisible)
    window.addEventListener('focus', onVisible)
    activate()
  })
  onActivated(activate)
  onDeactivated(() => {
    active = false
    stopTimer()
  })
  onBeforeUnmount(() => {
    disposed = true
    stopTimer()
    document.removeEventListener('visibilitychange', onVisible)
    window.removeEventListener('focus', onVisible)
  })
}
