import { describe, expect, it } from 'vitest'
import type { ChatExample } from './types'
import { localizeText, missingCapabilities, selectChatExamples } from './select'

function example(id: string, patch: Partial<ChatExample> = {}): ChatExample {
  return {
    id,
    category: 'research',
    icon: 'search',
    priority: 0,
    title: { en: id },
    prompt: { en: `prompt ${id}` },
    requires: [],
    surfaces: ['welcome', 'gallery'],
    ...patch,
  }
}

describe('localizeText', () => {
  it('matches a region locale by its language', () => {
    expect(localizeText({ en: 'Hi', zh: '你好' }, 'zh-CN')).toBe('你好')
  })

  it('falls back to en when the language is missing', () => {
    expect(localizeText({ en: 'Hi', zh: '你好' }, 'ja')).toBe('Hi')
  })
})

describe('missingCapabilities', () => {
  const mail = example('mail', { requires: ['email', 'browser'] })

  it('reports capabilities known to be missing', () => {
    expect(missingCapabilities(mail, { email: false, browser: true })).toEqual(['email'])
  })

  it('treats unknown capabilities as available', () => {
    expect(missingCapabilities(mail, {})).toEqual([])
  })
})

describe('selectChatExamples', () => {
  const catalog = [
    example('low', { priority: 1 }),
    example('high', { priority: 9 }),
    example('mail', { priority: 10, requires: ['email'] }),
    example('dev', { category: 'dev' }),
    example('onboard-only', { surfaces: ['onboarding'] }),
  ]
  const ids = (list: ChatExample[]) => list.map(item => item.id)

  it('keeps the surface, then sorts available first and by priority', () => {
    const picked = selectChatExamples(catalog, { surface: 'welcome', capabilities: { email: false } })
    expect(ids(picked)).toEqual(['high', 'low', 'dev', 'mail'])
  })

  it('drops unavailable examples when asked', () => {
    const picked = selectChatExamples(catalog, { surface: 'gallery', capabilities: { email: false }, hideUnavailable: true })
    expect(ids(picked)).toEqual(['high', 'low', 'dev'])
  })

  it('takes one example per category before repeating a category', () => {
    const picked = selectChatExamples(catalog, { surface: 'welcome', capabilities: {}, limit: 2, spreadCategories: true })
    expect(ids(picked)).toEqual(['mail', 'dev'])
  })

  it('fills remaining slots after spreading categories', () => {
    const picked = selectChatExamples(catalog, { surface: 'welcome', capabilities: {}, limit: 3, spreadCategories: true })
    expect(ids(picked)).toEqual(['mail', 'dev', 'high'])
  })

  it('filters by category', () => {
    const picked = selectChatExamples(catalog, { surface: 'gallery', capabilities: {}, category: 'dev' })
    expect(ids(picked)).toEqual(['dev'])
  })

  it('caps the result at the limit', () => {
    const picked = selectChatExamples(catalog, { surface: 'gallery', capabilities: {}, limit: 1 })
    expect(ids(picked)).toEqual(['mail'])
  })

  describe('with a seed', () => {
    const pool = Array.from({ length: 12 }, (_, i) => example(`e${i}`, { priority: i, category: (['research', 'dev', 'writing', 'team'] as const)[i % 4] }))
    const pick = (seed: number) => ids(selectChatExamples(pool, { surface: 'welcome', capabilities: {}, limit: 3, spreadCategories: true, seed }))

    it('returns the same picks for the same seed', () => {
      expect(pick(0.42)).toEqual(pick(0.42))
    })

    it('varies the picks across seeds instead of always leading with the highest priority', () => {
      const firsts = new Set(Array.from({ length: 20 }, (_, i) => pick(i / 20)[0]))
      expect(firsts.size).toBeGreaterThan(3)
    })

    it('still spreads categories', () => {
      const categories = pick(0.7).map(id => pool.find(item => item.id === id)!.category)
      expect(new Set(categories).size).toBe(3)
    })

    it('still lists available examples before unavailable ones', () => {
      const mixed = [...pool, example('mail', { priority: 99, requires: ['email'] })]
      for (let i = 0; i < 10; i++) {
        const picked = ids(selectChatExamples(mixed, { surface: 'welcome', capabilities: { email: false }, seed: i / 10 }))
        expect(picked.at(-1)).toBe('mail')
      }
    })
  })
})
