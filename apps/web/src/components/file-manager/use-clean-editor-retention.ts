import { computed, onScopeDispose, ref, toValue, watch, type ComputedRef, type MaybeRefOrGetter, type Ref } from 'vue'

export const WARM_CLEAN_EDITOR_CAPACITY = 4

interface WarmEditorSlot {
  warm: Ref<boolean>
}

// Insertion-ordered MRU pool for hidden *clean* editors only. Visible editors
// and editors with unsaved edits (`pinned`) stay mounted unconditionally outside
// this pool so Monaco's in-memory Undo/Redo stack is never discarded.
const warmPool = new Set<WarmEditorSlot>()

function promoteToWarm(slot: WarmEditorSlot) {
  warmPool.delete(slot)
  warmPool.add(slot)
  slot.warm.value = true
  while (warmPool.size > WARM_CLEAN_EDITOR_CAPACITY) {
    const oldest = warmPool.values().next().value
    if (!oldest) break
    warmPool.delete(oldest)
    oldest.warm.value = false
  }
}

function removeFromWarm(slot: WarmEditorSlot) {
  if (warmPool.delete(slot)) {
    slot.warm.value = false
  }
}

export function useCleanEditorRetention(
  visible: MaybeRefOrGetter<boolean>,
  pinned: MaybeRefOrGetter<boolean>,
): ComputedRef<boolean> {
  const slot: WarmEditorSlot = { warm: ref(false) }
  const hasOpened = ref(Boolean(toValue(visible) || toValue(pinned)))

  watch(
    [() => Boolean(toValue(visible)), () => Boolean(toValue(pinned))],
    ([isVisible, isPinned]) => {
      if (isVisible || isPinned) {
        hasOpened.value = true
        removeFromWarm(slot)
        return
      }
      if (hasOpened.value && !warmPool.has(slot)) {
        promoteToWarm(slot)
      }
    },
    { immediate: true },
  )

  onScopeDispose(() => {
    removeFromWarm(slot)
  })

  return computed(() => Boolean(toValue(visible) || toValue(pinned) || slot.warm.value))
}

/** @internal Vitest-only reset for module warm pool state. */
export function resetCleanEditorRetentionForTests() {
  for (const slot of warmPool) {
    slot.warm.value = false
  }
  warmPool.clear()
}
