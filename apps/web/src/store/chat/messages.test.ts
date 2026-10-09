import { describe, expect, it } from 'vitest'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en.json'
import zh from '@/i18n/locales/zh.json'
import ja from '@/i18n/locales/ja.json'
import { commandActionErrorMessage, commandErrorMessage, resolveCommandErrorMessage } from './messages'

// The command panel's lookup: vue-i18n copy in the current locale or English.
function panelLookup(locale: 'en' | 'zh' | 'ja') {
  const i18n = createI18n({ legacy: false, locale, fallbackLocale: 'en', messages: { en, zh, ja } })
  const { t, te } = i18n.global
  return { i18n, lookup: (key: string) => te(key) || te(key, 'en') ? t(key) : '' }
}

describe('command_error copy', () => {
  it('shows the generic copy, not the server message, for a code without copy', () => {
    const frame = { code: 'unknown.code', message: 'SECRET upstream text' }
    const { lookup } = panelLookup('en')
    expect(resolveCommandErrorMessage(frame, lookup)).toBe(en.chat.slash.errorMessages.generic)
    expect(commandActionErrorMessage(frame)).toBe(en.chat.slash.errorMessages.generic)
  })

  it('reads the catalog copy for the code, in the panel and the store alike', () => {
    const { lookup } = panelLookup('en')
    for (const [code, copy] of [['slash.unknown_command', en.errors.slash.unknown_command], ['runtime_control.failed', en.errors.runtime_control.failed]]) {
      const frame = { code, message: 'x' }
      expect(resolveCommandErrorMessage(frame, lookup)).toBe(copy)
      expect(commandActionErrorMessage(frame)).toBe(copy)
    }
  })

  it('gives a client-side command error the copy for its catalog code', () => {
    expect(commandErrorMessage('slash.attachments_unsupported')).toBe(en.errors.slash.attachments_unsupported)
  })

  it('follows a locale switch in the panel', () => {
    const { i18n, lookup } = panelLookup('en')
    const frame = { code: 'unknown.code', message: 'SECRET upstream text' }
    i18n.global.locale.value = 'zh'
    expect(resolveCommandErrorMessage(frame, lookup)).toBe(zh.chat.slash.errorMessages.generic)
  })
})
