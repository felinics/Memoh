import { getActivePinia } from 'pinia'
import { computed } from 'vue'
import { cssCodeFontFamilyStyleValue, DEFAULT_CODE_FONT_SIZE_PX } from '@/store/settings/typography'
import { DEFAULT_SHIKI_THEME_DARK, DEFAULT_SHIKI_THEME_LIGHT } from '@/store/settings/shiki-theme'
import { useSettingsStore } from '@/store/settings'

export type CodeSurfaceThemeMode = 'system' | 'light' | 'dark'
export type CodeSurfaceTheme = string | { dark: string, light: string }
export type CodeSurfaceBackground = 'card' | 'editor' | 'theme'

// The app's default code stack, including the CJK fallbacks the design system
// appends to every code face.
const DEFAULT_CODE_FONT_STACK = cssCodeFontFamilyStyleValue('')

/**
 * Which plane a surface paints on. stream-diffs declares `--diffs-bg` on its
 * own shadow host, so a value set on an ancestor never wins; it has to be
 * injected into the host's highest-priority layer. `theme` keeps the selected
 * Shiki theme's background (what the appearance previews show).
 */
export function codeSurfaceBackgroundCSS(background: CodeSurfaceBackground | undefined): string | undefined {
  if (background === 'card')
    return ':host{--diffs-bg:var(--card)}'
  if (background === 'editor')
    return ':host{--diffs-bg:var(--surface-editor)}'
  return undefined
}

/**
 * Stable identity for a theme value. Surfaces watch this instead of the object
 * so a parent re-render that rebuilds an equal `{ dark, light }` pair does not
 * tear down and re-create the surface (which would drop an open edit session).
 */
export function codeSurfaceThemeKey(theme: CodeSurfaceTheme | undefined): string {
  if (!theme)
    return ''
  return typeof theme === 'string' ? theme : `${theme.light}\u0000${theme.dark}`
}

/**
 * The appearance every code surface shares with the workspace: the user's
 * picked Shiki theme pair, the interface mode driving which half is active, and
 * the code typography (which the design system already exposes as CSS
 * variables; the surface components forward it to stream-diffs).
 *
 * Surfaces are also mounted outside an application (unit tests that render a
 * tool detail on its own), where no Pinia exists. Those get the same defaults
 * the settings store starts from instead of throwing.
 */
export function useCodeSurfaceAppearance() {
  const settings = getActivePinia() ? useSettingsStore() : null

  const theme = computed<{ dark: string, light: string }>(() => ({
    dark: settings?.shikiThemeDark ?? DEFAULT_SHIKI_THEME_DARK,
    light: settings?.shikiThemeLight ?? DEFAULT_SHIKI_THEME_LIGHT,
  }))
  const themeType = computed<CodeSurfaceThemeMode>(() => (settings?.isDark ? 'dark' : 'light'))
  const fontSize = computed(() => settings?.codeFontSizePx ?? DEFAULT_CODE_FONT_SIZE_PX)
  // Monaco used 1.7× for the same reason the editor typography does: CJK
  // glyphs read bottom-heavy inside stream-diffs' default 1.35× line box.
  const lineHeight = computed(() => Math.round(fontSize.value * 1.7))
  // Always the design-system stack: it carries the CJK fallbacks every code
  // surface in this app needs, whereas a library default (or a bare mono
  // family) would drop them.
  const fontFamily = computed(() => settings?.codeFontStack ?? DEFAULT_CODE_FONT_STACK)

  return { theme, themeType, fontSize, lineHeight, fontFamily }
}
