import { computed, nextTick, ref, watch, type ComputedRef } from 'vue'

interface ListboxRow {
  type: 'header' | 'item'
}

/**
 * Keyboard navigation for a virtualized listbox driven by a search input
 * (the combobox pattern). Virtualization keeps only the visible rows in the
 * DOM, so Tab alone can't reach off-screen options; this tracks an active row,
 * scrolls it into view (which mounts it), and exposes the state needed to wire
 * `aria-activedescendant`. Focus stays on the input throughout.
 */
export function useListboxKeyboard<TRow extends ListboxRow>(params: {
  rows: ComputedRef<TRow[]>
  scrollToIndex: (index: number) => void
  onSelect: (row: TRow, index: number) => void
}) {
  const activeIndex = ref(-1)

  // Row indexes that are selectable options (headers are skipped).
  const itemIndexes = computed(() => {
    const indexes: number[] = []
    params.rows.value.forEach((row, index) => {
      if (row.type === 'item') indexes.push(index)
    })
    return indexes
  })

  function activate(rowIndex: number) {
    activeIndex.value = rowIndex
    if (rowIndex >= 0) nextTick(() => params.scrollToIndex(rowIndex))
  }

  function step(delta: 1 | -1) {
    const items = itemIndexes.value
    if (items.length === 0) return
    const pos = items.indexOf(activeIndex.value)
    const next = pos === -1
      ? (delta === 1 ? 0 : items.length - 1)
      : Math.min(items.length - 1, Math.max(0, pos + delta))
    activate(items[next]!)
  }

  function reset() {
    activeIndex.value = -1
  }

  function onKeydown(event: KeyboardEvent) {
    switch (event.key) {
      case 'ArrowDown':
        event.preventDefault()
        step(1)
        break
      case 'ArrowUp':
        event.preventDefault()
        step(-1)
        break
      case 'Home':
        if (itemIndexes.value.length) {
          event.preventDefault()
          activate(itemIndexes.value[0]!)
        }
        break
      case 'End':
        if (itemIndexes.value.length) {
          event.preventDefault()
          activate(itemIndexes.value[itemIndexes.value.length - 1]!)
        }
        break
      case 'Enter': {
        const row = params.rows.value[activeIndex.value]
        if (row && row.type === 'item') {
          event.preventDefault()
          params.onSelect(row, activeIndex.value)
        }
        break
      }
    }
  }

  // Filtering rebuilds the row list, so a prior active index may now point at a
  // different row (or a header) — reset to avoid a stale highlight.
  watch(params.rows, reset)

  return { activeIndex, onKeydown, reset }
}
