import { effectScope, nextTick, ref } from 'vue'
import { beforeEach, describe, expect, it } from 'vitest'
import {
  resetCleanEditorRetentionForTests,
  useCleanEditorRetention,
  WARM_CLEAN_EDITOR_CAPACITY,
} from './use-clean-editor-retention'

function createSlot(initialVisible = false, initialPinned = false) {
  const visible = ref(initialVisible)
  const pinned = ref(initialPinned)
  const scope = effectScope()
  const retained = scope.run(() => useCleanEditorRetention(visible, pinned))!
  return { visible, pinned, retained, dispose: () => scope.stop() }
}

describe('useCleanEditorRetention', () => {
  beforeEach(() => {
    resetCleanEditorRetentionForTests()
  })

  it('does not mount cold background clean editors before first reveal', () => {
    const slot = createSlot(false, false)
    expect(slot.retained.value).toBe(false)
    slot.dispose()
  })

  it('keeps every visible editor mounted even past warm capacity', () => {
    const slots = Array.from({ length: WARM_CLEAN_EDITOR_CAPACITY + 3 }, () => createSlot(true, false))
    expect(slots.every(slot => slot.retained.value)).toBe(true)
    slots.forEach(slot => slot.dispose())
  })

  it('retains up to WARM_CLEAN_EDITOR_CAPACITY hidden clean editors in MRU order', async () => {
    const slots = Array.from({ length: WARM_CLEAN_EDITOR_CAPACITY + 1 }, () => createSlot(false, false))

    // Visit 0..3 and hide each — all 4 fit in the warm pool.
    for (let i = 0; i < WARM_CLEAN_EDITOR_CAPACITY; i++) {
      slots[i]!.visible.value = true
      await nextTick()
      slots[i]!.visible.value = false
      await nextTick()
    }
    expect(slots.slice(0, WARM_CLEAN_EDITOR_CAPACITY).map(s => s.retained.value)).toEqual([true, true, true, true])

    // Revisit slot 0 so slot 1 becomes the oldest warm clean editor.
    slots[0]!.visible.value = true
    await nextTick()
    slots[0]!.visible.value = false
    await nextTick()

    // Visit and hide slot 4 — slot 1 is evicted while slot 0 stays warm.
    slots[WARM_CLEAN_EDITOR_CAPACITY]!.visible.value = true
    await nextTick()
    slots[WARM_CLEAN_EDITOR_CAPACITY]!.visible.value = false
    await nextTick()

    expect(slots[0]!.retained.value).toBe(true)
    expect(slots[1]!.retained.value).toBe(false)
    expect(slots[2]!.retained.value).toBe(true)
    expect(slots[3]!.retained.value).toBe(true)
    expect(slots[4]!.retained.value).toBe(true)

    slots.forEach(slot => slot.dispose())
  })

  it('never evicts pinned (dirty) hidden editors and never lets them starve the clean warm pool', async () => {
    const dirtyA = createSlot(true, true)
    const dirtyB = createSlot(true, true)
    dirtyA.visible.value = false
    dirtyB.visible.value = false
    await nextTick()

    const cleanSlots = Array.from({ length: WARM_CLEAN_EDITOR_CAPACITY + 1 }, () => createSlot(false, false))
    for (const slot of cleanSlots) {
      slot.visible.value = true
      await nextTick()
      slot.visible.value = false
      await nextTick()
    }

    // Both dirty editors remain mounted so Monaco preserves their Undo/Redo stack.
    expect(dirtyA.retained.value).toBe(true)
    expect(dirtyB.retained.value).toBe(true)
    // Clean warm pool still retains its full 4 MRU slots; only the 5th clean editor was evicted.
    expect(cleanSlots[0]!.retained.value).toBe(false)
    expect(cleanSlots.slice(1).every(s => s.retained.value)).toBe(true)

    dirtyA.dispose()
    dirtyB.dispose()
    cleanSlots.forEach(slot => slot.dispose())
  })

  it('moves a hidden editor into the clean warm pool when unpinned after background save', async () => {
    const cleanSlots = Array.from({ length: WARM_CLEAN_EDITOR_CAPACITY }, () => createSlot(false, false))
    for (const slot of cleanSlots) {
      slot.visible.value = true
      await nextTick()
      slot.visible.value = false
      await nextTick()
    }

    const dirtyHidden = createSlot(true, true)
    dirtyHidden.visible.value = false
    await nextTick()
    expect(dirtyHidden.retained.value).toBe(true)
    expect(cleanSlots[0]!.retained.value).toBe(true)

    // Saving in the background clears `pinned` while still hidden: it joins the warm pool and evicts the oldest clean slot.
    dirtyHidden.pinned.value = false
    await nextTick()
    expect(dirtyHidden.retained.value).toBe(true)
    expect(cleanSlots[0]!.retained.value).toBe(false)

    dirtyHidden.dispose()
    cleanSlots.forEach(slot => slot.dispose())
  })

  it('frees its warm slot when disposed', async () => {
    const slots = Array.from({ length: WARM_CLEAN_EDITOR_CAPACITY }, () => createSlot(false, false))
    for (const slot of slots) {
      slot.visible.value = true
      await nextTick()
      slot.visible.value = false
      await nextTick()
    }
    slots[0]!.dispose()

    const extra = createSlot(true, false)
    extra.visible.value = false
    await nextTick()

    expect(extra.retained.value).toBe(true)
    expect(slots.slice(1).every(s => s.retained.value)).toBe(true)

    extra.dispose()
    slots.slice(1).forEach(slot => slot.dispose())
  })
})
