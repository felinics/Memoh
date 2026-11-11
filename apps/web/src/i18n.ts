import { createI18n } from 'vue-i18n'
import { computed } from 'vue'
import { detectLocale } from '@/utils/detect-locale'
import { isChunkLoadError, reloadForChunkLoadError } from '@/lib/chunk-load-error'
import { showBootFailure } from '@/lib/boot-splash'
import { loadLocaleMessages, loadedLocaleMessages, type Locale, type LocaleMessages } from '@/i18n/messages'

export type { Locale, LocaleMessages }

// Language names in the language picker are endonyms — each language written
// in itself — and must NOT go through t(). Translating them made the same
// option read "中文" in the English UI and "简体中文" in the Chinese UI, so
// users looking for their own language saw a label that changed with the UI.
// Keep this list the only source of these labels; don't add langXx keys back
// to the locale JSON files.
export const LOCALE_OPTIONS: ReadonlyArray<{ value: Locale, label: string }> = [
  { value: 'en', label: 'English' },
  { value: 'zh', label: '简体中文' },
  { value: 'ja', label: '日本語' },
]

function getInitialLocale(): Locale {
  try {
    const stored = localStorage.getItem('language')
    if (stored === 'en' || stored === 'zh' || stored === 'ja') return stored
    const detected = detectLocale()
    // Write detected locale so that the settings store's useStorage picks it up
    // instead of overwriting with its 'en' default.
    localStorage.setItem('language', detected)
    return detected
  } catch {
    // Storage can be unavailable (blocked site data); use the browser language.
    return detectLocale()
  }
}

const initialLocale = getInitialLocale()

// Languages are downloaded on demand (see i18n/messages.ts). Any already loaded
// when this module runs are registered right away; the rest are added by
// prepareLocale as they load.
const i18n = createI18n<[LocaleMessages], Locale, false>({
  locale: initialLocale,
  legacy: false,
  fallbackLocale: 'en',
  messages: Object.fromEntries(
    LOCALE_OPTIONS.flatMap(({ value }) => {
      const messages = loadedLocaleMessages(value)
      return messages ? [[value, messages]] : []
    }),
  ) as Record<Locale, LocaleMessages>,
})

export default i18n

/** Loads a language and registers it with vue-i18n, so switching to it shows its copy. */
export function prepareLocale(locale: Locale): Promise<void> {
  return loadLocaleMessages(locale).then((messages) => {
    if (!i18n.global.availableLocales.includes(locale)) {
      i18n.global.setLocaleMessage(locale, messages)
    }
  })
}

/**
 * Starts downloading every language, for screens with the language picker. The
 * download usually finishes before the user picks, so the pick applies at once
 * instead of waiting on it. A failure is ignored here; picking that language
 * downloads it again.
 */
export function preloadLocales(): void {
  for (const { value } of LOCALE_OPTIONS) loadLocaleMessages(value).catch(() => {})
}

/**
 * Settles once the starting language is loaded; the app mounts after it so the
 * first render has its copy. A chunk that fails to load reloads the page, the
 * same recovery the router applies to route chunks, at most once a minute
 * (see reloadForChunkLoadError); failing again within that, the loading splash
 * switches to its failure state (showBootFailure). Either way it stays pending so the app
 * doesn't mount without copy. Any other failure is logged and the app mounts
 * anyway.
 */
export const i18nReady: Promise<void> = prepareLocale(initialLocale).catch((error: unknown) => {
  if (isChunkLoadError(error)) {
    if (reloadForChunkLoadError()) {
      console.warn('[i18n] Locale chunk failed to load, reloading...', error)
    } else {
      console.error('[i18n] Locale chunk failed to load again after a reload', error)
      showBootFailure(error instanceof Error ? error.message : String(error))
    }
    return new Promise<void>(() => {})
  }
  console.error('[i18n] Failed to load locale messages', error)
})

const t = i18n.global.t

export const i18nRef = (arg:string) => {
  return computed(() => {
    return t(arg)
  })
}
