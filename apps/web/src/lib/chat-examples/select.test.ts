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
})
