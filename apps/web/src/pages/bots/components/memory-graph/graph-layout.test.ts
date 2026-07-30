import { describe, expect, it } from 'vitest'
import { fitGraphToView, wheelZoomScale } from './graph-layout'

describe('fitGraphToView', () => {
  it('centers and scales to fit bounding box', () => {
    const positions = new Map([
      ['a', { x: 0, y: 0 }],
      ['b', { x: 100, y: 100 }],
    ])
    const fit = fitGraphToView(positions, { width: 400, height: 400 })
    expect(fit).not.toBeNull()
    expect(fit?.center).toEqual([50, 50])
    expect(fit?.zoom).toBeGreaterThan(0)
    expect(fit?.zoom).toBeLessThanOrEqual(2)
  })

  it('returns null for empty positions or viewport', () => {
    expect(fitGraphToView(new Map(), { width: 400, height: 400 })).toBeNull()
    expect(fitGraphToView(new Map([['a', { x: 0, y: 0 }]]), { width: 0, height: 0 })).toBeNull()
  })
})

describe('wheelZoomScale', () => {
  it('zooms in for negative delta', () => {
    expect(wheelZoomScale(-100, 0, 800, 0.002)).toBeGreaterThan(1)
  })

  it('zooms out for positive delta', () => {
    expect(wheelZoomScale(100, 0, 800, 0.002)).toBeLessThan(1)
  })

  it('returns 1 for zero delta', () => {
    expect(wheelZoomScale(0, 0, 800, 0.002)).toBe(1)
  })
})
