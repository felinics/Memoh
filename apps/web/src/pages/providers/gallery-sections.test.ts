import { describe, expect, it } from 'vitest'
import { pageSections } from './gallery-sections'

const sections = [
  { key: 'configured', items: ['a', 'b'] },
  { key: 'subscriptions', items: ['c', 'd', 'e'] },
  { key: 'others', items: ['f', 'g', 'h', 'i', 'j', 'k'] },
]

describe('pageSections', () => {
  it('fills a page across sections in order', () => {
    expect(pageSections(sections, 1, 4)).toEqual([
      { key: 'configured', items: ['a', 'b'] },
      { key: 'subscriptions', items: ['c', 'd'] },
    ])
  })

  it('repeats the section a page starts inside', () => {
    expect(pageSections(sections, 2, 4)).toEqual([
      { key: 'subscriptions', items: ['e'] },
      { key: 'others', items: ['f', 'g', 'h'] },
    ])
    expect(pageSections(sections, 3, 4)).toEqual([
      { key: 'others', items: ['i', 'j', 'k'] },
    ])
  })

  it('skips empty sections and pages past the end', () => {
    expect(pageSections([{ key: 'configured', items: [] }, ...sections.slice(1)], 1, 2)).toEqual([
      { key: 'subscriptions', items: ['c', 'd'] },
    ])
    expect(pageSections(sections, 4, 4)).toEqual([])
  })
})
