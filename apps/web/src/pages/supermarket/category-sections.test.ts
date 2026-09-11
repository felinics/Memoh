import { describe, expect, it } from 'vitest'
import type { HandlersSupermarketAppCategory } from '@memohai/sdk'
import { browsableCategories, categoryAppCount } from './category-sections'

function category(id: string, order: number, registries: Array<{ id: string; count: number }>): HandlersSupermarketAppCategory {
  return {
    id,
    name: id,
    names: { en: id },
    order,
    app_count: registries.reduce((sum, entry) => sum + entry.count, 0),
    registries,
  }
}

describe('categoryAppCount', () => {
  const documents = category('documents', 50, [{ id: 'memoh', count: 3 }, { id: 'openai', count: 2 }])

  it('counts every registry by default', () => {
    expect(categoryAppCount(documents)).toBe(5)
  })

  it('narrows to one registry', () => {
    expect(categoryAppCount(documents, 'openai')).toBe(2)
    expect(categoryAppCount(documents, 'missing')).toBe(0)
  })
})

describe('browsableCategories', () => {
  const categories = [
    category('tool', 30, [{ id: 'openai', count: 4 }]),
    category('agent', 10, [{ id: 'memoh', count: 2 }]),
    category('other', 999, []),
    category('runtime', 20, [{ id: 'memoh', count: 3 }]),
  ]

  it('drops empty categories and keeps table order', () => {
    expect(browsableCategories(categories).map(item => item.id)).toEqual(['agent', 'runtime', 'tool'])
  })

  it('drops categories empty for the selected registry', () => {
    expect(browsableCategories(categories, 'memoh').map(item => item.id)).toEqual(['agent', 'runtime'])
  })
})
