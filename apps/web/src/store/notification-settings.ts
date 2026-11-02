import { computed, onScopeDispose, ref } from 'vue'
import { defineStore } from 'pinia'
import { useLocalStorage } from '@vueuse/core'

/** Notification permission belongs to this device; dots and unread cursors are independent. */
export const useNotificationSettings = defineStore('notification-settings', () => {
  const desktop = typeof window !== 'undefined' && !!window.api?.desktop?.notify
  const supported = desktop || (typeof Notification !== 'undefined' && window.isSecureContext)
  const enabled = useLocalStorage('memoh-system-notifications', desktop)
  const permission = ref<NotificationPermission>(typeof Notification === 'undefined' ? 'default' : Notification.permission)
  const pending = ref(false)
  const active = computed(() => enabled.value && supported && (desktop || permission.value === 'granted'))
  const refresh = () => {
    if (typeof Notification !== 'undefined') permission.value = Notification.permission
  }
  window.addEventListener('focus', refresh)
  onScopeDispose(() => window.removeEventListener('focus', refresh))

  /** Called directly by a switch gesture so browsers accept the permission request. */
  async function setEnabled(value: boolean) {
    if (!value) { enabled.value = false; return }
    if (!supported) return
    if (desktop) { enabled.value = true; return }
    pending.value = true
    try {
      permission.value = await Notification.requestPermission()
      enabled.value = permission.value === 'granted'
    } catch {
      enabled.value = false
      refresh()
    } finally {
      pending.value = false
    }
  }
  return { active, desktop, supported, enabled, permission, pending, setEnabled }
})
