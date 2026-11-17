import { bundledThemesInfo } from 'shiki/themes'
import type { BundledTheme } from 'shiki'

export type ShikiThemeVariant = 'light' | 'dark'

export interface BundledShikiTheme {
  id: BundledTheme
  displayName: string
  type: ShikiThemeVariant
}

export const DEFAULT_SHIKI_THEME_LIGHT: BundledTheme = 'github-light'
export const DEFAULT_SHIKI_THEME_DARK: BundledTheme = 'github-dark'

const themesById = new Map<string, BundledShikiTheme>(
  bundledThemesInfo.map(info => [
    info.id,
    { id: info.id as BundledTheme, displayName: info.displayName, type: info.type },
  ]),
)

export function listBundledShikiThemes(): BundledShikiTheme[] {
  return Array.from(themesById.values())
}

export function normalizeShikiTheme(value: unknown, variant: ShikiThemeVariant): BundledTheme {
  if (typeof value === 'string') {
    const info = themesById.get(value)
    if (info && info.type === variant) return info.id
  }
  return variant === 'light' ? DEFAULT_SHIKI_THEME_LIGHT : DEFAULT_SHIKI_THEME_DARK
}
