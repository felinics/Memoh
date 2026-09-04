import { computed, onBeforeUnmount, ref, shallowRef, watch, type Ref } from 'vue'
import { visibleRowRange, type VisibleRowRange } from './trajectory-model'

const ROW_HEIGHT_REM = 1.75

function rootFontSize(): number {
  if (typeof document === 'undefined') return 16
  const size = Number.parseFloat(getComputedStyle(document.documentElement).fontSize)
  return Number.isFinite(size) && size > 0 ? size : 16
}

// Fixed-height rows keep the window arithmetic exact, so only the viewport
// plus overscan is ever mounted no matter how many rows the ledger holds. The
// row height follows the root font size so the stride scales with text.
export function useVirtualRows(container: Ref<HTMLElement | null>, count: Ref<number>, overscan = 8) {
  const scrollTop = ref(0)
  const viewportHeight = ref(0)
  const rowHeight = ref(ROW_HEIGHT_REM * rootFontSize())
  const range = shallowRef<VisibleRowRange>({ start: 0, end: 0, offsetTop: 0, totalHeight: 0 })
  let observer: ResizeObserver | null = null
  let attached: HTMLElement | null = null

  function onScroll() {
    scrollTop.value = attached?.scrollTop ?? 0
  }

  function measure() {
    if (!attached) return
    viewportHeight.value = attached.clientHeight
    rowHeight.value = ROW_HEIGHT_REM * rootFontSize()
  }

  function detach() {
    attached?.removeEventListener('scroll', onScroll)
    observer?.disconnect()
    observer = null
    attached = null
  }

  watch(container, (element) => {
    detach()
    if (!element) return
    attached = element
    element.addEventListener('scroll', onScroll, { passive: true })
    scrollTop.value = element.scrollTop
    measure()
    if (typeof ResizeObserver !== 'undefined') {
      observer = new ResizeObserver(measure)
      observer.observe(element)
    }
  }, { immediate: true, flush: 'post' })

  onBeforeUnmount(detach)

  const next = computed(() => visibleRowRange({
    scrollTop: scrollTop.value,
    viewportHeight: viewportHeight.value,
    rowHeight: rowHeight.value,
    count: count.value,
    overscan,
  }))
  // Only a changed window publishes, so a scroll inside the current window
  // does not re-slice the rows.
  watch(next, (value) => {
    const current = range.value
    if (value.start !== current.start || value.end !== current.end || value.totalHeight !== current.totalHeight) {
      range.value = value
    }
  }, { immediate: true })

  // Rows prepended above the viewport would shift the reader; keep the same
  // row under the eye by moving the scroll offset with them.
  function keepAnchored(prependedRows: number) {
    if (!attached || prependedRows <= 0) return
    attached.scrollTop += prependedRows * rowHeight.value
    scrollTop.value = attached.scrollTop
  }

  return { range, rowHeight, keepAnchored }
}
