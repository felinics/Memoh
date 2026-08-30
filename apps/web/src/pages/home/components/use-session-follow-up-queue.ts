import { computed, getCurrentScope, onScopeDispose, ref, toValue, watch, type MaybeRefOrGetter } from 'vue'
import {
  deleteFollowUpQueueItem,
  deleteSteerQueueItem,
  fetchSteerQueue,
  fetchFollowUpQueue,
  queueItemText,
  reorderFollowUpQueue,
  reorderSteerQueue,
  promoteFollowUpQueueItemToSteer,
  updateSteerQueueItem,
  updateFollowUpQueueItem,
  type SessionQueueItem,
} from '@/composables/api/useChat.chat-api'

export interface EditableFollowUpQueueItem extends SessionQueueItem {
  text: string
  queueKind: 'follow-up' | 'steer'
}

function editable(item: SessionQueueItem, queueKind: EditableFollowUpQueueItem['queueKind']): EditableFollowUpQueueItem {
  return { ...item, text: queueItemText(item), queueKind }
}

export function useSessionFollowUpQueue(
  botId: MaybeRefOrGetter<string | null | undefined>,
  sessionId: MaybeRefOrGetter<string | null | undefined>,
  active: MaybeRefOrGetter<boolean> = false,
) {
  const items = ref<EditableFollowUpQueueItem[]>([])
  const loading = ref(false)
  const busy = ref(new Set<string>())
  const hasItems = computed(() => items.value.length > 0)
  let requestVersion = 0
  let refreshTimer: ReturnType<typeof setInterval> | undefined

  const markBusy = (id: string, value: boolean) => {
    const next = new Set(busy.value)
    if (value) next.add(id)
    else next.delete(id)
    busy.value = next
  }

  async function refresh() {
    const bot = String(toValue(botId) ?? '').trim()
    const session = String(toValue(sessionId) ?? '').trim()
    const version = ++requestVersion
    if (!bot || !session) {
      items.value = []
      return
    }
    loading.value = true
    try {
      const [followUps, steers] = await Promise.all([
        fetchFollowUpQueue(bot, session),
        fetchSteerQueue(bot, session),
      ])
      if (version === requestVersion) {
        // Steers are always consumed before follow-ups. Keeping both in one
        // visual list makes a promoted follow-up stay visible immediately,
        // while the server APIs and pending order remain separate.
        items.value = [
          ...steers.map(item => editable(item, 'steer')),
          ...followUps.map(item => editable(item, 'follow-up')),
        ]
      }
    } finally {
      if (version === requestVersion) loading.value = false
    }
  }

  async function update(item: EditableFollowUpQueueItem) {
    const bot = String(toValue(botId) ?? '').trim()
    const session = String(toValue(sessionId) ?? '').trim()
    const id = item.item_id?.trim()
    const text = item.text.trim()
    if (!bot || !session || !id) return
    if (!text) {
      await refresh()
      return
    }
    markBusy(id, true)
    try {
      const updated = item.queueKind === 'steer'
        ? await updateSteerQueueItem(bot, session, id, text)
        : await updateFollowUpQueueItem(bot, session, id, text)
      const index = items.value.findIndex(entry => entry.item_id === id)
      if (index >= 0) items.value[index] = editable(updated, item.queueKind)
    } catch (error) {
      await refresh()
      throw error
    } finally {
      markBusy(id, false)
    }
  }

  async function remove(item: EditableFollowUpQueueItem) {
    const bot = String(toValue(botId) ?? '').trim()
    const session = String(toValue(sessionId) ?? '').trim()
    const id = item.item_id?.trim()
    if (!bot || !session || !id) return
    markBusy(id, true)
    try {
      if (item.queueKind === 'steer') await deleteSteerQueueItem(bot, session, id)
      else await deleteFollowUpQueueItem(bot, session, id)
      items.value = items.value.filter(entry => entry.item_id !== id)
    } finally {
      markBusy(id, false)
    }
  }

  async function steer(item: EditableFollowUpQueueItem) {
    const bot = String(toValue(botId) ?? '').trim()
    const session = String(toValue(sessionId) ?? '').trim()
    const id = item.item_id?.trim()
    if (!bot || !session || !id) return
    markBusy(id, true)
    try {
      await promoteFollowUpQueueItemToSteer(bot, session, id)
      const index = items.value.findIndex(entry => entry.item_id === id)
      if (index >= 0) items.value[index] = editable(items.value[index]!, 'steer')
      await refresh()
    } catch (error) {
      await refresh()
      throw error
    } finally {
      markBusy(id, false)
    }
  }

  async function reorder(oldIndex: number, newIndex: number) {
    const moved = items.value[oldIndex]
    if (!moved?.item_id || oldIndex === newIndex) return
    const ordered = items.value.slice()
    ordered.splice(oldIndex, 1)
    ordered.splice(newIndex, 0, moved)
    items.value = ordered
    const beforeId = ordered
      .slice(newIndex + 1)
      .find(entry => entry.queueKind === moved.queueKind)?.item_id ?? ''
    const bot = String(toValue(botId) ?? '').trim()
    const session = String(toValue(sessionId) ?? '').trim()
    if (!bot || !session) return
    markBusy(moved.item_id, true)
    try {
      items.value = moved.queueKind === 'steer'
        ? (await reorderSteerQueue(bot, session, moved.item_id, beforeId)).map(item => editable(item, 'steer'))
        : (await reorderFollowUpQueue(bot, session, moved.item_id, beforeId)).map(item => editable(item, 'follow-up'))
    } catch (error) {
      await refresh()
      throw error
    } finally {
      markBusy(moved.item_id, false)
    }
  }

  watch([() => toValue(botId), () => toValue(sessionId)], refresh, { immediate: true })

  function stopAutoRefresh() {
    if (refreshTimer === undefined) return
    clearInterval(refreshTimer)
    refreshTimer = undefined
  }

  function syncAutoRefresh() {
    stopAutoRefresh()
    if (!toValue(active) || !hasItems.value) return
    refreshTimer = setInterval(() => {
      if (loading.value || (typeof document !== 'undefined' && document.visibilityState === 'hidden')) return
      void refresh().catch(() => {})
    }, 1000)
  }

  watch([() => toValue(active), hasItems], syncAutoRefresh)
  if (getCurrentScope()) onScopeDispose(stopAutoRefresh)

  return { items, loading, busy, hasItems, refresh, update, remove, steer, reorder }
}
