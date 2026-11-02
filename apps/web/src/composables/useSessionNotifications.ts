import { onScopeDispose, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { useUnreadStore, type UnreadEntry } from '@/store/unread'
import { useChatStore } from '@/store/chat-list'
import { useNotificationSettings } from '@/store/notification-settings'
import { isSessionViewed } from './useSessionViewing'
import { sessionAttentionPath } from '@/utils/session-attention'
import { onAuthSessionCleared } from '@/lib/auth-session'

/** Only live attention is eligible. Other tabs may be viewing it, even when this tab is hidden. */
export function useSessionNotifications() {
  const unread = useUnreadStore()
  const chat = useChatStore()
  const settings = useNotificationSettings()
  const router = useRouter()
  const { t } = useI18n()
  const channel = typeof BroadcastChannel === 'undefined' ? null : new BroadcastChannel('memoh-session-attention')
  const probes = new Map<string, () => void>()
  let disposed = false
  let generation = 0

  if (channel) {
    channel.onmessage = ({ data }: MessageEvent<unknown>) => {
      if (!data || typeof data !== 'object') return
      const message = data as { type?: string, id?: string, sessionId?: string }
      if (typeof message.id !== 'string' || typeof message.sessionId !== 'string') return
      if (message.type === 'viewing' && isSessionViewed(message.sessionId)) {
        channel.postMessage({ ...message, type: 'viewed' })
      } else if (message.type === 'viewed') {
        probes.get(message.id)?.()
      }
    }
  }

  async function viewedElsewhere(entry: UnreadEntry): Promise<boolean> {
    if (!channel) return false
    const id = `${entry.sessionId}@${entry.attentionAt}`
    return new Promise(resolve => {
      const timer = window.setTimeout(() => { probes.delete(id); resolve(false) }, 120)
      probes.set(id, () => { window.clearTimeout(timer); probes.delete(id); resolve(true) })
      channel.postMessage({ type: 'viewing', id, sessionId: entry.sessionId })
    })
  }

  async function deliver(entry: UnreadEntry) {
    const expected = generation
    if (!settings.active || isSessionViewed(entry.sessionId)) return
    if (await viewedElsewhere(entry)) return
    if (disposed || expected !== generation || !settings.active || isSessionViewed(entry.sessionId)
      || unread.entries[entry.sessionId]?.attentionAt !== entry.attentionAt) return
    const bot = chat.bots.find(item => item.id === entry.botId)
    const title = `${bot?.display_name || bot?.name || 'Memoh'} · ${entry.title || t('chat.untitledSession')}`.slice(0, 256)
    const body = t(`notifications.kind.${entry.kind}`)
    if (settings.desktop) {
      await window.api?.desktop?.notify?.({ ...entry, title, body })
      return
    }
    const send = () => {
      if (disposed || expected !== generation || !settings.active || isSessionViewed(entry.sessionId)
        || unread.entries[entry.sessionId]?.attentionAt !== entry.attentionAt) return
      const key = `memoh-notified:${entry.sessionId}`
      try {
        const previous = localStorage.getItem(key)
        if (previous && Date.parse(previous) >= Date.parse(entry.attentionAt)) return
      } catch { /* Storage can be unavailable in private browsing. The notification tag still replaces duplicates. */ }
      const notification = new Notification(title, { body, tag: `memoh:${entry.sessionId}` })
      try { localStorage.setItem(key, entry.attentionAt) } catch { /* Permission does not depend on storage access. */ }
      notification.onclick = () => {
        notification.close()
        window.focus()
        void router.push(sessionAttentionPath(entry.botId, entry.sessionId))
      }
    }
    if (navigator.locks) await navigator.locks.request('memoh-system-notification', send)
    else send()
  }

  const unsubscribe = unread.onAttention(entry => {
    void deliver(entry).catch(error => console.warn('Failed to deliver session notification:', error))
  })
  watch(() => unread.total, count => {
    void window.api?.desktop?.setBadgeCount?.(count).catch(error => console.warn('Failed to update unread badge:', error))
  }, { immediate: true })
  const clearAuth = onAuthSessionCleared(() => { generation += 1 })
  onScopeDispose(() => {
    disposed = true
    unsubscribe()
    clearAuth()
    for (const resolve of [...probes.values()]) resolve()
    channel?.close()
  })
}
