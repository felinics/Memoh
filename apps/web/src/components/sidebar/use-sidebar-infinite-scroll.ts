import { computed, nextTick, ref, watch, type ComputedRef, type Ref } from 'vue'
import { useIntersectionObserver, useResizeObserver } from '@vueuse/core'

/**
 * Sidebar list pagination gates.
 *
 * If the load-more sentinel mounts while content still fits the viewport,
 * IntersectionObserver (+ rootMargin) treats it as already intersecting and
 * fires a page fetch. When appended rows then grow scrollHeight, overflow
 * anchoring (even with overflow-anchor: none on some paths) and layout can
 * still shift scroll position — lists open mid-scroll.
 *
 * Only observe the sentinel once content actually overflows the viewport.
 * While it still fits, prefetch pages until it overflows or the cursor ends.
 */

export function shouldShowLoadMoreSentinel(opts: {
  hasMore: boolean
  contentHeight: number
  viewportHeight: number
}): boolean {
  return (
    opts.hasMore
    && opts.viewportHeight > 0
    && opts.contentHeight > opts.viewportHeight
  )
}

export function shouldPrefetchToFillViewport(opts: {
  hasMore: boolean
  loading: boolean
  contentHeight: number
  viewportHeight: number
}): boolean {
  if (!opts.hasMore || opts.loading) return false
  if (opts.viewportHeight <= 0) return false
  return opts.contentHeight <= opts.viewportHeight
}

/**
 * After loadMore settles, only continue viewport prefetch when the attempt made
 * progress. A failed request leaves cursor + contentHeight unchanged; without
 * this gate the loading flag flipping false retriggers the watch in a tight loop.
 */
export function didLoadMoreMakeProgress(opts: {
  wasLoadingMore: boolean
  isLoadingMore: boolean
  previousCursor: string | null
  currentCursor: string | null
  previousContentHeight: number
  currentContentHeight: number
  previousItemCount?: number
  currentItemCount?: number
}): boolean {
  if (!opts.wasLoadingMore || opts.isLoadingMore) return true
  if (opts.previousCursor !== opts.currentCursor) return true
  if (opts.previousContentHeight !== opts.currentContentHeight) return true
  if (
    opts.previousItemCount !== undefined
    && opts.currentItemCount !== undefined
    && opts.previousItemCount !== opts.currentItemCount
  ) return true
  return false
}

export interface UseSidebarInfiniteScrollOptions {
  scrollEl: Ref<HTMLElement | null>
  hasMore: Ref<boolean> | ComputedRef<boolean>
  loading: Ref<boolean> | ComputedRef<boolean>
  loadMore: () => void | Promise<void>
  /** Cursor (Recents) or other stable token that advances when a page fetch succeeds. */
  progressCursor?: Ref<string | null> | ComputedRef<string | null>
  /** Visible row count — helps detect progress when client filters hide new rows. */
  itemCount?: Ref<number> | ComputedRef<number>
  enableViewportPrefetch?: boolean
}

export function useSidebarInfiniteScroll(options: UseSidebarInfiniteScrollOptions) {
  const loadMoreSentinel = ref<HTMLElement | null>(null)
  const contentHeight = ref(0)
  const viewportHeight = ref(0)

  function measureScroll() {
    const el = options.scrollEl.value
    contentHeight.value = el?.scrollHeight ?? 0
    viewportHeight.value = el?.clientHeight ?? 0
  }

  useResizeObserver(options.scrollEl, () => {
    measureScroll()
  })

  watch(
    () => [options.hasMore.value, options.loading.value, options.itemCount?.value] as const,
    () => {
      void nextTick(measureScroll)
    },
    { flush: 'post' },
  )

  const showSentinel = computed(() => shouldShowLoadMoreSentinel({
    hasMore: options.hasMore.value,
    contentHeight: contentHeight.value,
    viewportHeight: viewportHeight.value,
  }))

  useIntersectionObserver(
    loadMoreSentinel,
    ([entry]) => {
      if (!entry?.isIntersecting) return
      void options.loadMore()
    },
    {
      root: options.scrollEl,
      rootMargin: '0px 0px 200px 0px',
      threshold: 0,
    },
  )

  if (options.enableViewportPrefetch !== false) {
    watch(
      [
        options.hasMore,
        options.loading,
        contentHeight,
        viewportHeight,
        () => options.progressCursor?.value ?? null,
        () => options.itemCount?.value ?? null,
      ] as const,
      (curr, prev) => {
        if (!shouldPrefetchToFillViewport({
          hasMore: options.hasMore.value,
          loading: options.loading.value,
          contentHeight: contentHeight.value,
          viewportHeight: viewportHeight.value,
        })) return
        if (
          prev
          && !didLoadMoreMakeProgress({
            wasLoadingMore: prev[1],
            isLoadingMore: curr[1],
            previousCursor: prev[4],
            currentCursor: curr[4],
            previousContentHeight: prev[2],
            currentContentHeight: curr[2],
            previousItemCount: prev[5] ?? undefined,
            currentItemCount: curr[5] ?? undefined,
          })
        ) return
        void options.loadMore()
      },
    )
  }

  function resetScrollTop() {
    options.scrollEl.value?.scrollTo({ top: 0 })
  }

  return {
    loadMoreSentinel,
    showSentinel,
    resetScrollTop,
    measureScroll,
  }
}
