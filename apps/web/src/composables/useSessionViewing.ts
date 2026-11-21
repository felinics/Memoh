import { computed, getCurrentInstance, onActivated, onDeactivated, onScopeDispose, ref, watch, type Ref } from 'vue'
import { useUnreadStore } from '@/store/unread'

const viewers = new Set<(sessionId: string) => boolean>()

/** Check live DOM focus and pane state; notification delivery can precede Vue's next flush. */
export function isSessionViewed(sessionId: string): boolean {
  return [...viewers].some(viewer => viewer(sessionId))
}

/**
 * The caller gates activity on route, pane selection, visibility and transcript readiness.
 * A component cached by KeepAlive keeps its last props, so deactivation alone stops it reading.
 */
export function useSessionViewing(sessionId: Ref<string | null>, active: Ref<boolean>) {
  const unread = useUnreadStore()
  const focusRevision = ref(0)
  const refresh = () => { focusRevision.value += 1 }
  const attached = ref(true)
  if (getCurrentInstance()) {
    onActivated(() => { attached.value = true })
    onDeactivated(() => { attached.value = false })
  }
  const viewing = (id: string) => attached.value && active.value && sessionId.value === id
    && document.visibilityState === 'visible' && document.hasFocus()
  viewers.add(viewing)
  window.addEventListener('focus', refresh)
  window.addEventListener('blur', refresh)
  document.addEventListener('visibilitychange', refresh)

  const viewedSession = computed(() => {
    void focusRevision.value
    const id = sessionId.value
    return id && viewing(id) ? id : null
  })
  /**
   * The session this pane shows, regardless of window focus. A change here is an "open":
   * the first moment it is then actually viewed reads even a session the user marked
   * unread. Marking the session already on screen unread leaves it alone until then,
   * while new attention arriving on screen is read straight away.
   */
  const opened = computed(() => attached.value && active.value ? sessionId.value : null)
  let openPending: string | null = null
  watch(opened, (id) => { openPending = id }, { immediate: true, flush: 'sync' })
  watch(() => {
    const id = viewedSession.value
    if (!id) return null
    const entry = unread.entries[id]
    return `${id}|${entry?.attentionAt ?? ''}|${entry?.marked === true}`
  }, (key) => {
    if (!key) return
    const [id, at, marked] = key.split('|') as [string, string, string]
    const opening = openPending === id
    if (opening) openPending = null
    if (at && (marked !== 'true' || opening)) void unread.markRead(id)
  }, { immediate: true })

  onScopeDispose(() => {
    viewers.delete(viewing)
    window.removeEventListener('focus', refresh)
    window.removeEventListener('blur', refresh)
    document.removeEventListener('visibilitychange', refresh)
  })
  return viewedSession
}
