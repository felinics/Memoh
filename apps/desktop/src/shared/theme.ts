export const DESKTOP_THEME_SOURCES = ['system', 'light', 'dark'] as const

export type DesktopThemeSource = typeof DESKTOP_THEME_SOURCES[number]

export function normalizeDesktopThemeSource(value: unknown): DesktopThemeSource {
  return DESKTOP_THEME_SOURCES.includes(value as DesktopThemeSource)
    ? value as DesktopThemeSource
    : 'system'
}
