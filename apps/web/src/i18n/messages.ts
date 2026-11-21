import type en from './locales/en.json'

export type Locale = 'en' | 'zh' | 'ja'

export type LocaleMessages = typeof en

// Each language is its own chunk. Only the active one is downloaded before the
// first screen, so a first visit doesn't wait on the other two (about 55KB gzip
// each); they download in the background afterwards (prefetchAppPages in
// routes.ts). No fallback language is loaded alongside the active one: every
// locale has exactly the keys of en (enforced by i18n.test.ts).
//
// The chunk is requested when the entry script runs, not preloaded from
// index.html. Measured on Slow 4G, a preload competed with the entry download
// and made the chat page about 0.35s slower; requested later, it downloads
// while the app waits on its first API round trips.
//
// This module holds the loaded messages without depending on vue-i18n, so code
// that reads copy outside components (API error copy, chat notices) works the
// same in tests that mock vue-i18n. i18n.ts registers each loaded language with
// vue-i18n.
const localeLoaders: Record<Locale, () => Promise<{ default: LocaleMessages }>> = {
  en: () => import('./locales/en.json'),
  zh: () => import('./locales/zh.json'),
  ja: () => import('./locales/ja.json'),
}

const loadedMessages = new Map<Locale, LocaleMessages>()
const pendingMessages = new Map<Locale, Promise<LocaleMessages>>()
let shownLocale: Locale | undefined

/** Downloads a language's messages once; a failed download is retried by the next call. */
export function loadLocaleMessages(locale: Locale): Promise<LocaleMessages> {
  let pending = pendingMessages.get(locale)
  if (!pending) {
    pending = localeLoaders[locale]().then((module) => {
      loadedMessages.set(locale, module.default)
      return module.default
    })
    pending.catch(() => pendingMessages.delete(locale))
    pendingMessages.set(locale, pending)
  }
  return pending
}

/** A language's messages if they have finished loading. */
export function loadedLocaleMessages(locale: Locale): LocaleMessages | undefined {
  return loadedMessages.get(locale)
}

/** Records the language the page currently shows; messagesFor falls back to it. */
export function setShownLocale(locale: Locale): void {
  shownLocale = locale
}

/**
 * Messages for copy read outside vue-i18n's t(): the requested language once
 * it has loaded, otherwise the language the page currently shows, so the copy
 * matches the rest of the page. A language picked in another tab is requested
 * before this tab has downloaded it; meanwhile the page still shows the
 * previous one, and other languages may already be loaded in the background.
 * The app mounts only after its starting language has loaded, so something is
 * always loaded by the time a component or event handler asks.
 */
export function messagesFor(locale: Locale): LocaleMessages {
  const messages = loadedMessages.get(locale)
    ?? (shownLocale && loadedMessages.get(shownLocale))
    ?? loadedMessages.get('en')
    ?? loadedMessages.values().next().value
  if (!messages) throw new Error('[i18n] Locale messages read before any language loaded')
  return messages
}
