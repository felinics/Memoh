import { describe, expect, it } from 'vitest'
import { rankBetween } from './lexorank'

describe('rankBetween', () => {
  it('seeds an empty group', () => {
    const r = rankBetween('', '')
    expect(r.length).toBeGreaterThan(0)
    expect(r.endsWith('0')).toBe(false)
  })

  it('stays strictly between bounds', () => {
    const cases: Array<[string, string]> = [
      ['', 'i'],
      ['i', ''],
      ['a', 'b'],
      ['a', 'a1'],
      ['0z', '1'],
      ['az', 'b'],
      ['z', ''],
    ]
    for (const [prev, next] of cases) {
      const mid = rankBetween(prev, next)
      if (prev !== '') expect(mid > prev, `${mid} > ${prev}`).toBe(true)
      if (next !== '') expect(mid < next, `${mid} < ${next}`).toBe(true)
      expect(mid.endsWith('0')).toBe(false)
    }
  })

  it('rejects out-of-order bounds', () => {
    expect(() => rankBetween('b', 'a')).toThrow()
    expect(() => rankBetween('a', 'a')).toThrow()
  })

  it('survives repeated insertion at the same gap', () => {
    let prev = ''
    const upper = 'i'
    for (let i = 0; i < 50; i++) {
      const mid = rankBetween(prev, upper)
      expect(mid > prev).toBe(true)
      expect(mid < upper).toBe(true)
      prev = mid
    }
  })
})
