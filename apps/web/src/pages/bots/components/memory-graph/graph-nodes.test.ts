import { describe, expect, it } from 'vitest'
import { graphNodeSize, parseRgbChannels, subjectAccent } from './graph-nodes'
import type { ChartTheme } from './types'

const theme: ChartTheme = {
  line: '#ccc',
  fallback: { core: '#111', disk: '#eee' },
  fontFamily: 'sans-serif',
  palette: [
    { core: '#f00', disk: '#fdd' },
    { core: '#0f0', disk: '#dfd' },
  ],
  tooltip: '#000',
  tooltipForeground: '#fff',
  label: '#111',
}

describe('parseRgbChannels', () => {
  it('parses rgb strings', () => {
    expect(parseRgbChannels('rgb(10, 20, 30)')).toEqual([10, 20, 30])
  })

  it('returns null for non-rgb', () => {
    expect(parseRgbChannels('#fff')).toBeNull()
  })
})

describe('graphNodeSize', () => {
  it('defaults small counts to base size', () => {
    expect(graphNodeSize(1)).toBe(34)
  })

  it(' grows with count up to cap', () => {
    expect(graphNodeSize(64)).toBeLessThanOrEqual(48)
    expect(graphNodeSize(64)).toBeGreaterThan(34)
  })
})

describe('subjectAccent', () => {
  it('returns fallback for empty subject', () => {
    expect(subjectAccent(undefined, theme)).toEqual(theme.fallback)
  })

  it('is stable for the same subject', () => {
    expect(subjectAccent('billing', theme)).toEqual(subjectAccent('billing', theme))
  })
})
