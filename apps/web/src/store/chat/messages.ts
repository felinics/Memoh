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

// A first send that ended without the server's answer: it may have been taken.
export function sendOutcomeUnknownMessage() {
  return localizedMessages().chat.sendOutcomeUnknown
}

export function workdirMismatchMessage() {
  return localizedMessages().chat.sendWorkdirUnsupported
}

// The copy for a command error the client raises itself, by its catalog code.
export function commandErrorMessage(code: string) {
  return commandActionErrorMessage({ code })
}

// The copy for a command_error frame, read through lookup, which returns the
// copy for an i18n key or '' when there is none. The code is a catalog code
// with copy under errors.<code>; a code without copy gets the generic
// slash-command copy. The frame's message is server text and is never shown.
export function resolveCommandErrorMessage(error: { code?: string } | undefined, lookup: (key: string) => string) {
  const code = error?.code?.trim() ?? ''
  const copy = code ? lookup(`errors.${code}`) : ''
  return copy || lookup('chat.slash.errorMessages.generic')
}

// The copy for a command_error frame in the stored locale.
export function commandActionErrorMessage(error?: { code?: string }) {
  return resolveCommandErrorMessage(error, key => renderI18nMessage(key)) || localizedMessages().chat.slash.errorMessages.generic
}

export function forkFailedMessage() {
  return localizedMessages().chat.forkFailed
}
