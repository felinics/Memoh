import { describe, expect, it } from 'vitest'
import {
  DEFAULT_SHIKI_THEME_DARK,
  DEFAULT_SHIKI_THEME_LIGHT,
  listBundledShikiThemes,
  normalizeShikiTheme,
} from './shiki-theme'

describe('shiki-theme settings', () => {
  it('exposes GitHub themes as defaults', () => {
    expect(DEFAULT_SHIKI_THEME_LIGHT).toBe('github-light')
    expect(DEFAULT_SHIKI_THEME_DARK).toBe('github-dark')
  })

  it('keeps a matching variant', () => {
    expect(normalizeShikiTheme('solarized-light', 'light')).toBe('solarized-light')
    expect(normalizeShikiTheme('one-dark-pro', 'dark')).toBe('one-dark-pro')
  })

  it('falls back to default when the variant does not match the slot', () => {
    expect(normalizeShikiTheme('github-dark', 'light')).toBe(DEFAULT_SHIKI_THEME_LIGHT)
    expect(normalizeShikiTheme('github-light', 'dark')).toBe(DEFAULT_SHIKI_THEME_DARK)
  })

  it('falls back to default for unknown or empty values', () => {
    expect(normalizeShikiTheme(undefined, 'light')).toBe(DEFAULT_SHIKI_THEME_LIGHT)
    expect(normalizeShikiTheme(null, 'dark')).toBe(DEFAULT_SHIKI_THEME_DARK)
    expect(normalizeShikiTheme('', 'light')).toBe(DEFAULT_SHIKI_THEME_LIGHT)
    expect(normalizeShikiTheme('totally-fake', 'dark')).toBe(DEFAULT_SHIKI_THEME_DARK)
    expect(normalizeShikiTheme(123, 'light')).toBe(DEFAULT_SHIKI_THEME_LIGHT)
  })

  it('lists every bundled theme exactly once with its variant', () => {
    const themes = listBundledShikiThemes()
    const ids = themes.map(t => t.id)
    expect(new Set(ids).size).toBe(ids.length)
    expect(ids).toContain('github-light')
    expect(ids).toContain('github-dark')
    for (const theme of themes) {
      expect(theme.type === 'light' || theme.type === 'dark').toBe(true)
      expect(theme.displayName.length).toBeGreaterThan(0)
    }
  })
})
