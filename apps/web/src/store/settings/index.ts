import type { Locale } from '@/i18n'
import { computed, watch } from 'vue'
import { defineStore } from 'pinia'
import { useColorMode, useStorage } from '@vueuse/core'
import { useI18n } from 'vue-i18n'
import type { BundledTheme } from 'shiki'
import { isColorSchemeId, type ColorSchemeId } from '@/constants/color-schemes'
import {
  applyTypographyVariables,
  cssFontFamilyDeclaration,
  cssCodeFontFamilyStyleValue,
  DEFAULT_CODE_FONT_FAMILY,
  DEFAULT_CODE_FONT_SIZE_PX,
  DEFAULT_UI_FONT_FAMILY,
  DEFAULT_UI_FONT_SIZE_PX,
  normalizeCodeFontSizePx,
  normalizeStoredFontFamilyInput,
  normalizeUiFontSizePx,
} from './typography'
import {
  DEFAULT_SHIKI_THEME_DARK,
  DEFAULT_SHIKI_THEME_LIGHT,
  normalizeShikiTheme,
  type ShikiThemeVariant,
} from './shiki-theme'
import { DEFAULT_MERMAID_THEME, isMermaidTheme, type MermaidTheme } from './mermaid'

export type ThemePreference = 'light' | 'dark' | 'system'

export interface Settings {
  language: Locale;
  theme: ThemePreference;
  colorScheme: ColorSchemeId;
  uiFontFamily: string;
  codeFontFamily: string;
  uiFontSizePx: number;
  codeFontSizePx: number;
  shikiThemeLight: BundledTheme;
  shikiThemeDark: BundledTheme;
  mermaidTheme: MermaidTheme;
}

export const useSettingsStore = defineStore('settings', () => {
  const colorMode = useColorMode({ emitAuto: true })
  const i18n = useI18n()
  const defaultUiFontFamily = computed(() => DEFAULT_UI_FONT_FAMILY)
  const defaultCodeFontFamily = computed(() => DEFAULT_CODE_FONT_FAMILY)
  const language = useStorage<Locale>('language', 'en')
  const theme = useStorage<ThemePreference>('theme', 'system')
  const colorScheme = useStorage<ColorSchemeId>('color-scheme', 'memoh')
  const uiFontFamily = useStorage<string>('ui-font-family', '')
  const codeFontFamily = useStorage<string>('code-font-family', '')
  const uiFontSizePx = useStorage<number>('ui-font-size-px', DEFAULT_UI_FONT_SIZE_PX)
  const codeFontSizePx = useStorage<number>('code-font-size-px', DEFAULT_CODE_FONT_SIZE_PX)
  const shikiThemeLight = useStorage<BundledTheme>('shiki-theme-light', DEFAULT_SHIKI_THEME_LIGHT)
  const shikiThemeDark = useStorage<BundledTheme>('shiki-theme-dark', DEFAULT_SHIKI_THEME_DARK)
  const mermaidTheme = useStorage<MermaidTheme>('mermaid-theme', DEFAULT_MERMAID_THEME)
  const uiFontStack = computed(() => cssFontFamilyDeclaration(uiFontFamily.value, DEFAULT_UI_FONT_FAMILY))
  const codeFontStack = computed(() => cssCodeFontFamilyStyleValue(codeFontFamily.value))
  const shikiThemes = computed(() => ({ light: shikiThemeLight.value, dark: shikiThemeDark.value }))

  // Expose the resolved active color mode as the single source of truth.
  // 'system' is resolved against the OS/browser preference so consumers don't
  // need to re-implement that logic. isDark is a derived convenience for APIs
  // that only accept a boolean.
  const resolvedColorMode = computed(() => colorMode.state.value)
  const isDark = computed(() => resolvedColorMode.value === 'dark')

  const applyColorScheme = (value: ColorSchemeId) => {
    if (typeof document === 'undefined') return
    document.documentElement.dataset.colorScheme = value
  }

  const normalizeTypographySettings = () => {
    uiFontFamily.value = normalizeStoredFontFamilyInput(uiFontFamily.value, DEFAULT_UI_FONT_FAMILY)
    codeFontFamily.value = normalizeStoredFontFamilyInput(codeFontFamily.value, DEFAULT_CODE_FONT_FAMILY)
    const normalizedUiSize = normalizeUiFontSizePx(uiFontSizePx.value)
    const normalizedCodeSize = normalizeCodeFontSizePx(codeFontSizePx.value)
    if (uiFontSizePx.value !== normalizedUiSize) uiFontSizePx.value = normalizedUiSize
    if (codeFontSizePx.value !== normalizedCodeSize) codeFontSizePx.value = normalizedCodeSize
  }

  const applyTypography = () => {
    applyTypographyVariables({
      uiFontFamily: uiFontFamily.value,
      codeFontFamily: codeFontFamily.value,
      uiFontSizePx: uiFontSizePx.value,
      codeFontSizePx: codeFontSizePx.value,
    })
  }

  if (!isColorSchemeId(colorScheme.value)) {
    colorScheme.value = 'memoh'
  }
  shikiThemeLight.value = normalizeShikiTheme(shikiThemeLight.value, 'light')
  shikiThemeDark.value = normalizeShikiTheme(shikiThemeDark.value, 'dark')
  if (!isMermaidTheme(mermaidTheme.value)) {
    mermaidTheme.value = DEFAULT_MERMAID_THEME
  }
  normalizeTypographySettings()

  watch(theme, (value) => {
    colorMode.value = value === 'system' ? 'auto' : value
  }, { immediate: true })

  watch(language, (value) => {
    i18n.locale.value = value
    // Reflect the active locale onto <html lang> so locale-scoped CSS can target
    // it — chiefly the CJK font-weight de-emphasis (see :lang(zh) in style.css):
    // CJK glyphs render visually heavier than Latin at the same numeric weight, so
    // Chinese UI needs a lighter rung than English to read at the same density.
    if (typeof document !== 'undefined') {
      document.documentElement.lang = value
    }
  }, { immediate: true })

  watch(colorScheme, (value) => {
    if (!isColorSchemeId(value)) {
      colorScheme.value = 'memoh'
      return
    }
    applyColorScheme(value)
  }, { immediate: true })

  watch([uiFontFamily, codeFontFamily, uiFontSizePx, codeFontSizePx], () => {
    normalizeTypographySettings()
    applyTypography()
  }, { immediate: true })

  const setLanguage = (value: Locale) => {
    language.value = value
  }

  const withViewTransition = (fn: () => void) => {
    if (typeof document !== 'undefined' && 'startViewTransition' in document) {
      (document as Document & { startViewTransition: (cb: () => void) => unknown }).startViewTransition(fn)
    } else {
      fn()
    }
  }

  const setTheme = (value: ThemePreference) => {
    // No view transition here: the segmented control already animates its own
    // thumb, and wrapping each toggle in startViewTransition freezes the page for
    // the transition's duration — which made rapid segment switching feel laggy
    // and swallowed hover. Theme flip is applied instantly instead.
    theme.value = value
    colorMode.value = value === 'system' ? 'auto' : value
  }

  const setColorScheme = (value: ColorSchemeId) => {
    withViewTransition(() => {
      colorScheme.value = value
      applyColorScheme(value)
    })
  }

  const setUiFontFamily = (value: string) => {
    uiFontFamily.value = normalizeStoredFontFamilyInput(value, DEFAULT_UI_FONT_FAMILY)
  }

  const setCodeFontFamily = (value: string) => {
    codeFontFamily.value = normalizeStoredFontFamilyInput(value, DEFAULT_CODE_FONT_FAMILY)
  }

  const setUiFontSizePx = (value: string | number) => {
    uiFontSizePx.value = normalizeUiFontSizePx(value)
  }

  const setCodeFontSizePx = (value: string | number) => {
    codeFontSizePx.value = normalizeCodeFontSizePx(value)
  }

  const setShikiTheme = (variant: ShikiThemeVariant, value: BundledTheme) => {
    const next = normalizeShikiTheme(value, variant)
    if (variant === 'light') shikiThemeLight.value = next
    else shikiThemeDark.value = next
  }

  const setMermaidTheme = (value: MermaidTheme) => {
    if (!isMermaidTheme(value)) return
    mermaidTheme.value = value
  }

  return {
    language,
    theme,
    colorScheme,
    uiFontFamily,
    codeFontFamily,
    uiFontSizePx,
    codeFontSizePx,
    shikiThemeLight,
    shikiThemeDark,
    shikiThemes,
    resolvedColorMode,
    isDark,
    mermaidTheme,
    defaultUiFontFamily,
    defaultCodeFontFamily,
    uiFontStack,
    codeFontStack,
    setLanguage,
    setTheme,
    setColorScheme,
    setUiFontFamily,
    setCodeFontFamily,
    setUiFontSizePx,
    setCodeFontSizePx,
    setShikiTheme,
    setMermaidTheme,
  }
})

export type { MermaidTheme } from './mermaid'
export { MERMAID_THEMES, DEFAULT_MERMAID_THEME } from './mermaid'
