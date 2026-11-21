import type { Locale } from '@/i18n'

export function detectLocale(): Locale {
  const lang = navigator.language || ''
  const normalized = lang.toLowerCase()
  if (normalized.startsWith('zh')) return 'zh'
  if (normalized.startsWith('ja')) return 'ja'
  return 'en'
}
