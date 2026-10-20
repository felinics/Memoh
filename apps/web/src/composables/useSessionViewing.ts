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
  watch(() => {
    const id = viewedSession.value
    return id && unread.entries[id] ? [id, unread.entries[id].attentionAt] as const : null
  }, (entry) => {
    if (entry) void unread.markRead(entry[0])
  }, { immediate: true })

  onScopeDispose(() => {
    viewers.delete(viewing)
    window.removeEventListener('focus', refresh)
    window.removeEventListener('blur', refresh)
    document.removeEventListener('visibilitychange', refresh)
  })
  return viewedSession
}
