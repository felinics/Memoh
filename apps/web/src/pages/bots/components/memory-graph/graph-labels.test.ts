import { describe, expect, it } from 'vitest'
import {
  escapeTooltip,
  isTechnicalSlug,
  labelOpacityForZoom,
  nodeCaption,
  shouldShowZoomLabel,
  truncateLabel,
} from './graph-labels'
import type { GraphNode } from './types'

describe('labelOpacityForZoom', () => {
  it('returns 0 below fade start', () => {
    expect(labelOpacityForZoom(1)).toBe(0)
  })

  it('returns 1 at or above fade full', () => {
    expect(labelOpacityForZoom(2)).toBe(1)
  })

  it('ramps linearly between thresholds', () => {
    const mid = (1.2 + 1.85) / 2
    expect(labelOpacityForZoom(mid)).toBeCloseTo(0.5, 5)
  })
})

describe('truncateLabel', () => {
  it('truncates long strings with ellipsis', () => {
    expect(truncateLabel('abcdefghijklmnopqrst', 10)).toBe('abcdefghi…')
  })

  it('leaves short strings intact', () => {
    expect(truncateLabel('short')).toBe('short')
  })
})

describe('nodeCaption', () => {
  it('prefers memory text', () => {
    const node = { memory: 'hello', label: 'world' } as GraphNode
    expect(nodeCaption(node)).toBe('hello')
  })
})

describe('isTechnicalSlug', () => {
  it('detects mem-* slugs', () => {
    expect(isTechnicalSlug('mem-abc')).toBe(true)
    expect(isTechnicalSlug('topic')).toBe(false)
  })
})

describe('escapeTooltip', () => {
  it('escapes html', () => {
    expect(escapeTooltip('<b>&"\n')).toBe('&lt;b&gt;&amp;&quot;<br>')
  })
})

describe('shouldShowZoomLabel', () => {
  it('hides labels while hovering', () => {
    expect(shouldShowZoomLabel(true, 2)).toBe(false)
  })

  it('shows when zoomed in and not hovering', () => {
    expect(shouldShowZoomLabel(false, 2)).toBe(true)
  })
})
