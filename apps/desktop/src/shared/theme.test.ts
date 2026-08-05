import { describe, expect, it } from 'vitest'
import { normalizeDesktopThemeSource } from './theme'

describe('normalizeDesktopThemeSource', () => {
  it.each(['system', 'light', 'dark'] as const)('accepts %s', (source) => {
    expect(normalizeDesktopThemeSource(source)).toBe(source)
  })

  it('falls back to the system appearance for invalid renderer input', () => {
    expect(normalizeDesktopThemeSource('sepia')).toBe('system')
    expect(normalizeDesktopThemeSource(null)).toBe('system')
  })
})
