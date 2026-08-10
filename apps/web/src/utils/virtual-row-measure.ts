import type { Virtualizer } from '@tanstack/vue-virtual'

// measureVirtualRow is the shared `measureElement` option for measured-row
// virtualizers (sidebar Recents, model pickers).
//
// A row measured while it cannot be laid out — its panel is display:none
// (v-show'd sidebar view, folded section) or the node was just detached by a
// window shift — reports height 0. Recording that 0 poisons the virtualizer
// twice over: the spacer collapses, and the later 0→real re-measure runs
// scroll compensation ("content above the fold grew, push scrollTop down") on
// an element whose scrollTop cannot actually move, so the virtualizer's
// internal offset walks away from the element's real scrollTop one row at a
// time. The list then renders its window mid-list with a dead gap at the top
// until a real scroll event resyncs it.
//
// Zero is never a legitimate size for these rows (they all have a min-height),
// so treat it as "unmeasurable right now" and keep the last known size.
export function measureVirtualRow(
  el: HTMLElement,
  entry: ResizeObserverEntry | undefined,
  instance: Virtualizer<HTMLElement, HTMLElement>,
): number {
  const measured = entry?.borderBoxSize?.[0]?.blockSize
    ?? el.getBoundingClientRect().height
  if (measured > 0) return Math.round(measured)
  const index = instance.indexFromElement(el)
  const key = instance.options.getItemKey(index)
  return instance.itemSizeCache.get(key)
    ?? instance.options.estimateSize(index)
}
