import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en.json'
import zh from '@/i18n/locales/zh.json'
import ja from '@/i18n/locales/ja.json'
import { computed } from 'vue'
import { detectLocale } from '@/utils/detect-locale'

export type Locale = 'en' | 'zh' | 'ja'

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
  const stored = localStorage.getItem('language')
  if (stored === 'en' || stored === 'zh' || stored === 'ja') return stored
  const detected = detectLocale()
  // Write detected locale so that the settings store's useStorage picks it up
  // instead of overwriting with its 'en' default.
  localStorage.setItem('language', detected)
  return detected
}

const i18n = createI18n<typeof en | typeof zh | typeof ja, Locale>({
  locale: getInitialLocale(),
  legacy: false,
  fallbackLocale: 'en',
  messages: {
    en,
    zh,
    ja
  }
})

export default i18n

const t = i18n.global.t

export const i18nRef = (arg:string) => {
  return computed(() => {
    return t(arg)
  })
}
