/**
 * Sidebar Recents pagination gates.
 *
 * The virtualizer's spacer starts at 0 / a small estimate. If the load-more
 * sentinel is mounted in that window, IntersectionObserver (+ rootMargin)
 * treats it as already intersecting and fires a page fetch. When the real
 * spacer then grows above the sentinel, the browser's overflow anchoring
 * walks scrollTop down with the growth — Recents opens mid-list.
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
  itemCount: number
  contentHeight: number
  viewportHeight: number
}): boolean {
  if (!opts.hasMore || opts.loading || opts.itemCount === 0) return false
  if (opts.viewportHeight <= 0) return false
  return opts.contentHeight <= opts.viewportHeight
}
