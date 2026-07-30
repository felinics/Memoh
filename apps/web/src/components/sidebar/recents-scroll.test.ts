import { describe, expect, it } from 'vitest'
import { shouldPrefetchToFillViewport, shouldShowLoadMoreSentinel } from './recents-scroll'

describe('shouldShowLoadMoreSentinel', () => {
  it('stays off while the virtualizer spacer is still short of the viewport', () => {
    expect(shouldShowLoadMoreSentinel({
      hasMore: true,
      contentHeight: 0,
      viewportHeight: 400,
    })).toBe(false)
    expect(shouldShowLoadMoreSentinel({
      hasMore: true,
      contentHeight: 200,
      viewportHeight: 400,
    })).toBe(false)
  })

  it('turns on only after content overflows a measured viewport', () => {
    expect(shouldShowLoadMoreSentinel({
      hasMore: true,
      contentHeight: 401,
      viewportHeight: 400,
    })).toBe(true)
  })

  it('stays off when the viewport is not measured yet', () => {
    expect(shouldShowLoadMoreSentinel({
      hasMore: true,
      contentHeight: 800,
      viewportHeight: 0,
    })).toBe(false)
  })

  it('stays off when there is no next page', () => {
    expect(shouldShowLoadMoreSentinel({
      hasMore: false,
      contentHeight: 800,
      viewportHeight: 400,
    })).toBe(false)
  })
})

describe('shouldPrefetchToFillViewport', () => {
  it('loads the next page while a short list still fits the viewport', () => {
    expect(shouldPrefetchToFillViewport({
      hasMore: true,
      loading: false,
      contentHeight: 180,
      viewportHeight: 400,
    })).toBe(true)
  })

  it('does not prefetch once the list overflows', () => {
    expect(shouldPrefetchToFillViewport({
      hasMore: true,
      loading: false,
      contentHeight: 1200,
      viewportHeight: 400,
    })).toBe(false)
  })

  it('waits for a measured viewport and idle load state', () => {
    expect(shouldPrefetchToFillViewport({
      hasMore: true,
      loading: false,
      contentHeight: 180,
      viewportHeight: 0,
    })).toBe(false)
    expect(shouldPrefetchToFillViewport({
      hasMore: true,
      loading: true,
      contentHeight: 180,
      viewportHeight: 400,
    })).toBe(false)
  })

  it('keeps paging when the active filter yields zero visible rows', () => {
    // Mixed-type API page + Schedule/Agent client filter: spacer stays 0 while
    // hasMore is still true. Stopping here would strand an empty Recents list.
    expect(shouldPrefetchToFillViewport({
      hasMore: true,
      loading: false,
      contentHeight: 0,
      viewportHeight: 400,
    })).toBe(true)
  })
})
