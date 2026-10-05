import enMessages from '@/i18n/locales/en.json'
import zhMessages from '@/i18n/locales/zh.json'
import jaMessages from '@/i18n/locales/ja.json'
import { renderI18nMessage } from '@/utils/api-error'

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

// The copy for a command_error frame, read through lookup, which returns the
// copy for an i18n key or '' when there is none. Slash-command codes have their
// own copy; any other code is a catalog code with copy under errors.<code>. A
// code with neither gets the generic slash-command copy. The frame's message is
// server text and is never shown.
export function resolveCommandErrorMessage(error: { code?: string } | undefined, lookup: (key: string) => string) {
  const code = error?.code?.trim() ?? ''
  if (code) {
    for (const key of [`chat.slash.errorMessages.${code}`, `errors.${code}`]) {
      const copy = lookup(key)
      if (copy) return copy
    }
  }
  return lookup('chat.slash.errorMessages.generic')
}

// The copy for a command_error frame in the stored locale.
export function commandActionErrorMessage(error?: { code?: string }) {
  return resolveCommandErrorMessage(error, key => renderI18nMessage(key)) || commandErrorMessage('generic')
}

export function forkFailedMessage() {
  return localizedMessages().chat.forkFailed
}
