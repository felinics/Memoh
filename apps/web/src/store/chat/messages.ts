import enMessages from '@/i18n/locales/en.json'
import zhMessages from '@/i18n/locales/zh.json'
import jaMessages from '@/i18n/locales/ja.json'

function localizedMessages() {
  const storage = globalThis.localStorage
  const stored = typeof storage?.getItem === 'function'
    ? storage.getItem('language')
    : ''
  const locale = stored === 'zh' || stored === 'ja' ? stored : 'en'
  if (locale === 'zh') return zhMessages
  if (locale === 'ja') return jaMessages
  return enMessages
}

export function userInputConnectionLostMessage() {
  return localizedMessages().chat.tools.userInputConnectionLost
}

export function sendFailedMessage() {
  return localizedMessages().chat.sendFailed
}

export function commandErrorMessage(code: string) {
  const errors = localizedMessages().chat.slash.errorMessages as Record<string, string>
  return errors[code] || errors.generic || 'Slash command failed.'
}

export function forkFailedMessage() {
  return localizedMessages().chat.forkFailed
}
