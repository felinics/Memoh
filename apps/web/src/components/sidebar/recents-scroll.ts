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
  contentHeight: number
  viewportHeight: number
}): boolean {
  // Do not gate on visible row count: the API returns mixed session types and
  // Recents filters client-side (Schedule / Agent). A page can yield zero rows
  // for the active filter while nextCursor is still set — keep paging.
  if (!opts.hasMore || opts.loading) return false
  if (opts.viewportHeight <= 0) return false
  return opts.contentHeight <= opts.viewportHeight
}

/**
 * After loadMoreSessions settles, only continue viewport prefetch when the
 * attempt made progress. A failed request leaves cursor + contentHeight
 * unchanged; without this gate the loading flag flipping false retriggers the
 * watch in a tight retry loop.
 *
 * Cursor advance with unchanged height is still progress: Schedule/Agent
 * client filters can yield an empty page while nextCursor moves.
 */
export function didLoadMoreMakeProgress(opts: {
  wasLoadingMore: boolean
  isLoadingMore: boolean
  previousCursor: string | null
  currentCursor: string | null
  previousContentHeight: number
  currentContentHeight: number
}): boolean {
  if (!opts.wasLoadingMore || opts.isLoadingMore) return true
  return (
    opts.previousCursor !== opts.currentCursor
    || opts.previousContentHeight !== opts.currentContentHeight
  )
}
