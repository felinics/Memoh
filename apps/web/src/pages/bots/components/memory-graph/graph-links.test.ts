import { describe, expect, it } from 'vitest'
import { buildFocusLink, buildStaticLink, graphEdgeOpacity, graphEdgeWidth } from './graph-links'
import type { ChartTheme, GraphEdgeCandidate } from './types'

const theme: ChartTheme = {
  line: '#ccc',
  fallback: { core: '#111', disk: '#eee' },
  fontFamily: 'sans-serif',
  palette: [],
  tooltip: '#000',
  tooltipForeground: '#fff',
  label: '#111',
}

const edge: GraphEdgeCandidate = {
  source: 'a',
  target: 'b',
  strength: 2,
}

describe('graphEdgeWidth', () => {
  it('grows sublinearly with strength', () => {
    expect(graphEdgeWidth(1)).toBeLessThan(graphEdgeWidth(8))
    expect(graphEdgeWidth(100)).toBeLessThanOrEqual(3)
  })
})

describe('buildStaticLink', () => {
  it('marks edges silent and non-emphasis', () => {
    const link = buildStaticLink(edge, theme)
    expect(link.silent).toBe(true)
    expect(link.emphasis?.disabled).toBe(true)
  })
})

describe('buildFocusLink', () => {
  it('slightly boosts adjacent edge opacity without widening', () => {
    const base = buildStaticLink(edge, theme)
    const focused = buildFocusLink(edge, theme, 'a')
    expect(focused.lineStyle.width).toBe(base.lineStyle.width)
    expect(focused.lineStyle.opacity).toBeGreaterThan(base.lineStyle.opacity!)
  })

  it('dims non-adjacent edges', () => {
    const focused = buildFocusLink(edge, theme, 'x')
    expect(focused.lineStyle.opacity).toBe(0.05)
  })
})

describe('graphEdgeOpacity', () => {
  it('caps at 0.72', () => {
    expect(graphEdgeOpacity(999)).toBeLessThanOrEqual(0.72)
  })
})
