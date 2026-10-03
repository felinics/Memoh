import { computed, onScopeDispose, ref, watch, watchEffect, type InjectionKey, type Ref } from 'vue'
import type { SessionSummary } from '@/composables/api/useChat'

export const sessionSelectionKey: InjectionKey<ReturnType<typeof useSessionSelection>> = Symbol('session-selection')

// Selection belongs to this sidebar instance, never to persisted chat focus.
export function useSessionSelection(botId: Ref<string | null>, locked: Ref<boolean>) {
  const active = ref(false)
  const selectedIds = ref(new Set<string>())
  const rows = ref(new Map<symbol, SessionSummary>())
  const displayedIds = computed(() => new Set(
    [...rows.value.values()].filter(session => session.bot_id === botId.value).map(session => session.id),
  ))
  const allDisplayedSelected = computed(() => displayedIds.value.size > 0
    && [...displayedIds.value].every(id => selectedIds.value.has(id)))

  function clear() {
    active.value = false
    selectedIds.value = new Set()
  }

  watch(botId, clear, { flush: 'sync' })

  function toggle(id: string) {
    if (!active.value || locked.value || !displayedIds.value.has(id)) return
    if (selectedIds.value.has(id)) selectedIds.value.delete(id)
    else selectedIds.value.add(id)
  }

  function toggleDisplayed() {
    if (!active.value || locked.value) return
    const deselect = allDisplayedSelected.value
    for (const id of displayedIds.value) {
      if (deselect) selectedIds.value.delete(id)
      else selectedIds.value.add(id)
    }
  }

  // Only mounted rows participate. Collapsing a group unregisters its rows;
  // already checked IDs remain selected until explicitly cleared or deleted.
  function registerRow(session: () => SessionSummary) {
    const key = Symbol()
    watchEffect(() => { rows.value.set(key, session()) })
    onScopeDispose(() => { rows.value.delete(key) })
  }

  return { active, selectedIds, displayedIds, allDisplayedSelected, locked, clear, toggle, toggleDisplayed, registerRow }
}
