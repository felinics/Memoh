import { createI18n } from 'vue-i18n'
import { describe, expect, it } from 'vitest'
import en from '@/i18n/locales/en.json'
import ja from '@/i18n/locales/ja.json'
import zh from '@/i18n/locales/zh.json'

interface LocaleMessage {
  path: string
  value: string
}

const localeMessages = { en, ja, zh }

function collectLocaleMessages(value: unknown, path: string[] = []): LocaleMessage[] {
  if (typeof value === 'string') {
    return [{ path: path.join('.'), value }]
  }
  if (Array.isArray(value)) {
    return value.flatMap((item, index) => collectLocaleMessages(item, [...path, String(index)]))
  }
  if (value && typeof value === 'object') {
    return Object.entries(value).flatMap(([key, item]) => collectLocaleMessages(item, [...path, key]))
  }
  return []
}

describe('i18n locale messages', () => {
  for (const [locale, messages] of Object.entries(localeMessages)) {
    it(`${locale} messages compile`, () => {
      const entries = collectLocaleMessages(messages)
      const flattened = Object.fromEntries(
        entries.map(({ value }, index) => [`message_${index}`, value]),
      )
      const i18n = createI18n({
        legacy: false,
        locale,
        fallbackLocale: false,
        missingWarn: false,
        fallbackWarn: false,
        warnHtmlMessage: false,
        messages: { [locale]: flattened },
      })

      for (const [index, entry] of entries.entries()) {
        expect(
          () => i18n.global.t(`message_${index}`),
          `${locale}.${entry.path} should compile`,
        ).not.toThrow()
      }
    })
  }
})
