import { describe, expect, it } from 'vitest'
import type { Virtualizer } from '@tanstack/vue-virtual'
import { measureVirtualRow } from './virtual-row-measure'

type RowVirtualizer = Virtualizer<HTMLElement, HTMLElement>

function fakeInstance(overrides: {
  index?: number
  cachedSize?: number
  estimate?: number
} = {}): RowVirtualizer {
  const { index = 3, cachedSize, estimate = 36 } = overrides
  const itemSizeCache = new Map<string | number, number>()
  if (cachedSize !== undefined) itemSizeCache.set(`key-${index}`, cachedSize)
  return {
    indexFromElement: () => index,
    itemSizeCache,
    options: {
      getItemKey: (i: number) => `key-${i}`,
      estimateSize: () => estimate,
    },
  } as unknown as RowVirtualizer
}

function fakeRow(height: number): HTMLElement {
  return {
    getBoundingClientRect: () => ({ height }),
  } as unknown as HTMLElement
}

function fakeEntry(blockSize: number): ResizeObserverEntry {
  return {
    borderBoxSize: [{ blockSize, inlineSize: 200 }],
  } as unknown as ResizeObserverEntry
}

describe('measureVirtualRow', () => {
  it('returns the rounded observer box size when the row is laid out', () => {
    expect(measureVirtualRow(fakeRow(0), fakeEntry(35.6), fakeInstance())).toBe(36)
  })

  it('falls back to getBoundingClientRect without an observer entry', () => {
    expect(measureVirtualRow(fakeRow(38.2), undefined, fakeInstance())).toBe(38)
  })

  it('keeps the cached size for a hidden row instead of recording 0', () => {
    // display:none panels and just-detached rows measure 0; writing that into
    // the size cache desyncs the virtualizer's scroll compensation.
    const instance = fakeInstance({ cachedSize: 36 })
    expect(measureVirtualRow(fakeRow(0), fakeEntry(0), instance)).toBe(36)
  })

  it('falls back to the size estimate when a hidden row was never measured', () => {
    const instance = fakeInstance({ estimate: 32 })
    expect(measureVirtualRow(fakeRow(0), undefined, instance)).toBe(32)
  })
})
