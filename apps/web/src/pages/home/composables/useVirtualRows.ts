import { computed, onBeforeUnmount, ref, watch, type Ref } from 'vue'
import { visibleRowRange } from './trajectory-model'

// Fixed-height rows keep the window arithmetic exact, so only the viewport
// plus overscan is ever mounted no matter how many rows the ledger holds.
export function useVirtualRows(container: Ref<HTMLElement | null>, count: Ref<number>, rowHeight: number, overscan = 8) {
  const scrollTop = ref(0)
  const viewportHeight = ref(0)
  let observer: ResizeObserver | null = null
  let attached: HTMLElement | null = null

  function onScroll() {
    scrollTop.value = attached?.scrollTop ?? 0
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
    viewportHeight.value = element.clientHeight
    scrollTop.value = element.scrollTop
    if (typeof ResizeObserver !== 'undefined') {
      observer = new ResizeObserver(() => {
        viewportHeight.value = element.clientHeight
      })
      observer.observe(element)
    }
  }, { immediate: true, flush: 'post' })

  onBeforeUnmount(detach)

  const range = computed(() => visibleRowRange({
    scrollTop: scrollTop.value,
    viewportHeight: viewportHeight.value,
    rowHeight,
    count: count.value,
    overscan,
  }))

  function scrollToIndex(index: number) {
    const element = attached
    if (!element) return
    const top = index * rowHeight
    if (top < element.scrollTop || top + rowHeight > element.scrollTop + element.clientHeight) {
      element.scrollTop = Math.max(top - element.clientHeight / 2, 0)
    }
  }

  function scrollToBottom() {
    const element = attached
    if (element) element.scrollTop = element.scrollHeight
  }

  return { range, scrollToIndex, scrollToBottom }
}
