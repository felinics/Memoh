import { describe, expect, it } from 'vitest'
import { resolveStarterPromptSet, type StarterPromptSet } from './useStarterPrompts'

const held: StarterPromptSet = { hash: 'old', items: [{ title: 'Old', prompt: 'Old prompt' }] }
const fresh = { title: 'New', prompt: 'New prompt' }

describe('resolveStarterPromptSet', () => {
  it('keeps the held set when the server reports it unchanged', () => {
    expect(resolveStarterPromptSet(held, { unchanged: true, hash: 'old', items: [] })).toBe(held)
  })

  it('replaces the held set with a newly generated one', () => {
    expect(resolveStarterPromptSet(held, { unchanged: false, hash: 'new', items: [fresh] }))
      .toEqual({ hash: 'new', items: [fresh] })
  })

  it('keeps the held set when no new set could be generated', () => {
    expect(resolveStarterPromptSet(held, { unchanged: false, hash: '', items: [] })).toBe(held)
  })

  it('settles on an empty set when nothing is held and nothing was generated', () => {
    expect(resolveStarterPromptSet(undefined, { unchanged: false, hash: '', items: [] }))
      .toEqual({ hash: '', items: [] })
  })

  it('does not trust unchanged without a held set', () => {
    expect(resolveStarterPromptSet(undefined, { unchanged: true, hash: 'x', items: [] }))
      .toEqual({ hash: '', items: [] })
  })
})
