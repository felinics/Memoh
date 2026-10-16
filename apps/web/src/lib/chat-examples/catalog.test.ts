import { describe, expect, it } from 'vitest'
import { BUILTIN_CHAT_EXAMPLES } from './catalog'
import { CHAT_EXAMPLE_ICONS } from './icons'

describe('BUILTIN_CHAT_EXAMPLES', () => {
  it('has unique ids', () => {
    const ids = BUILTIN_CHAT_EXAMPLES.map(example => example.id)
    expect(new Set(ids).size).toBe(ids.length)
  })

  it('ships every title and prompt in en, zh and ja', () => {
    for (const example of BUILTIN_CHAT_EXAMPLES) {
      for (const text of [example.title, example.prompt]) {
        expect([text.en, text.zh, text.ja].every(Boolean), example.id).toBe(true)
      }
    }
  })

  it('offers enough examples for the welcome strip and the gallery', () => {
    const on = (surface: string) => BUILTIN_CHAT_EXAMPLES.filter(example => example.surfaces.includes(surface as never))
    expect(on('welcome').length).toBeGreaterThanOrEqual(3)
    expect(on('gallery').length).toBeGreaterThanOrEqual(on('welcome').length)
  })

  it('only offers capability-free examples during onboarding', () => {
    const onboarding = BUILTIN_CHAT_EXAMPLES.filter(example => example.surfaces.includes('onboarding'))
    expect(onboarding.length).toBeGreaterThanOrEqual(3)
    expect(onboarding.every(example => example.requires.length === 0)).toBe(true)
  })

  it('uses known icons', () => {
    for (const example of BUILTIN_CHAT_EXAMPLES) {
      expect(CHAT_EXAMPLE_ICONS[example.icon], example.id).toBeDefined()
    }
  })
})
