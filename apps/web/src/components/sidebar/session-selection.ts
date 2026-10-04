import { computed, ref, type ComputedRef, type InjectionKey, type Ref } from 'vue'

// Multi-select state for the sessions panel. panel-sessions provides ONE
// instance, so rows in Recents and in every folder check into the same set.
// There is no separate "selection mode" flag: the panel is selecting exactly
// while something is checked, so unchecking the last row also leaves the mode
// instead of stranding checkboxes with no action bar.
export interface SessionSelection {
  selectedIds: Readonly<Ref<ReadonlySet<string>>>
  active: ComputedRef<boolean>
  isSelected: (sessionId: string) => boolean
  select: (sessionId: string) => void
  toggle: (sessionId: string) => void
  remove: (sessionId: string) => void
  // Keeps only the ids that are both selected and listed — never re-adds one.
  retain: (sessionIds: readonly string[]) => void
  clear: () => void
}

export function createSessionSelection(): SessionSelection {
  const selectedIds = ref<ReadonlySet<string>>(new Set())

  function update(mutate: (next: Set<string>) => void) {
    const next = new Set(selectedIds.value)
    mutate(next)
    selectedIds.value = next
  }

  return {
    selectedIds,
    active: computed(() => selectedIds.value.size > 0),
    isSelected: sessionId => selectedIds.value.has(sessionId),
    select: sessionId => update(next => next.add(sessionId)),
    toggle: sessionId => update((next) => {
      if (!next.delete(sessionId)) next.add(sessionId)
    }),
    remove: (sessionId) => {
      if (selectedIds.value.has(sessionId)) update(next => next.delete(sessionId))
    },
    retain: (sessionIds) => {
      const keep = new Set(sessionIds)
      update((next) => {
        for (const id of next) if (!keep.has(id)) next.delete(id)
      })
    },
    clear: () => {
      if (selectedIds.value.size > 0) selectedIds.value = new Set()
    },
  }
}

export const SessionSelectionKey: InjectionKey<SessionSelection> = Symbol('session-selection')
