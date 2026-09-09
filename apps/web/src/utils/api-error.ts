import enMessages from '@/i18n/locales/en.json'
import zhMessages from '@/i18n/locales/zh.json'
import jaMessages from '@/i18n/locales/ja.json'

interface ResolveApiErrorMessageOptions {
  prefixFallback?: boolean
}

type ErrorRecord = Record<string, unknown>
type Locale = 'en' | 'zh' | 'ja'

export interface MemohError {
  code: string
  args: Record<string, unknown>
  message?: string
  requestId?: string
  status?: number
}

const messagesByLocale = {
  en: enMessages,
  zh: zhMessages,
  ja: jaMessages,
} as const

function asRecord(value: unknown): ErrorRecord | null {
  if (!value || typeof value !== 'object') {
    return null
  }
  return value as ErrorRecord
}

function collectErrorRecords(error: unknown, out: ErrorRecord[] = [], seen = new Set<unknown>()): ErrorRecord[] {
  const record = asRecord(error)
  if (!record || seen.has(record)) {
    return out
  }
  seen.add(record)
  out.push(record)

  for (const key of ['body', 'data', 'error', 'detail', 'response', 'feedback', 'message']) {
    collectErrorRecords(record[key], out, seen)
  }

  return out
}

function currentLocale(): Locale {
  try {
    const stored = globalThis.localStorage?.getItem('language')
    if (stored === 'en' || stored === 'zh' || stored === 'ja') return stored
  } catch {
    // Ignore storage failures; API error rendering should never make callers fail.
  }
  return 'en'
}

function lookupMessage(locale: Locale, key: string): string {
  let value: unknown = messagesByLocale[locale]
  for (const part of key.split('.')) {
    if (!value || typeof value !== 'object') return ''
    value = (value as Record<string, unknown>)[part]
  }
  return typeof value === 'string' ? value : ''
}

function formatMessage(template: string, args?: ErrorRecord): string {
  if (!args) return template
  return template.replace(/\{([^}]+)\}/g, (match, key: string) => {
    const value = args[key]
    if (typeof value === 'string' || typeof value === 'number' || typeof value === 'boolean') {
      return String(value)
    }
    return match
  })
}

function renderI18nMessage(key: string, args?: ErrorRecord): string {
  const trimmed = key.trim()
  const template = lookupMessage(currentLocale(), trimmed) || lookupMessage('en', trimmed)
  return template ? formatMessage(template, args).trim() : ''
}

function pickApiFeedbackMessage(error: unknown): string {
  for (const record of collectErrorRecords(error)) {
    const args = asRecord(record.args) ?? undefined

    const explicitKey = record.i18n_key ?? record.i18nKey
    if (typeof explicitKey === 'string' && explicitKey.trim()) {
      const rendered = renderI18nMessage(explicitKey, args)
      if (rendered) return rendered
    }

    const code = (typeof record.code === 'string' && record.code.trim())
      || (typeof record.error_code === 'string' && record.error_code.trim())
    if (code) {
      const rendered = renderI18nMessage(`errors.${code}`, args)
      if (rendered) return rendered
    }
  }
  return ''
}

function pickErrorDetail(error: unknown): string {
  if (typeof error === 'string' && error.trim()) {
    return error.trim()
  }

  for (const record of collectErrorRecords(error)) {
    for (const key of ['message', 'error', 'detail']) {
      const value = record[key]
      if (typeof value === 'string' && value.trim()) {
        return value.trim()
      }
    }
  }

  return ''
}

function pickNetworkErrorMessage(error: unknown): string {
  if (!(error instanceof TypeError)) return ''
  return renderI18nMessage('common.networkError')
}

export function parseMemohError(error: unknown): MemohError | null {
  for (const record of collectErrorRecords(error)) {
    const code = (typeof record.code === 'string' && record.code.trim())
      || (typeof record.error_code === 'string' && record.error_code.trim())
    if (!code) continue

    const status = typeof record.status === 'number'
      ? record.status
      : typeof record.http_status === 'number'
        ? record.http_status
        : undefined
    const requestId = record.request_id ?? record.requestId

    return {
      code,
      args: asRecord(record.args) ?? {},
      message: pickErrorDetail(record) || undefined,
      requestId: typeof requestId === 'string' && requestId.trim() ? requestId.trim() : undefined,
      status,
    }
  }
  return null
}

export function isApiErrorCode(error: unknown, code: string): boolean {
  return parseMemohError(error)?.code === code
}

export function apiErrorStatus(error: unknown): number | undefined {
  const parsed = parseMemohError(error)
  if (parsed?.status !== undefined) return parsed.status

  for (const record of collectErrorRecords(error)) {
    if (typeof record.status === 'number') return record.status
  }
  return undefined
}

export function resolveApiErrorMessage(
  error: unknown,
  fallback: string,
  options: ResolveApiErrorMessageOptions = {},
): string {
  const detail = pickApiFeedbackMessage(error)
    || pickNetworkErrorMessage(error)
    || pickErrorDetail(error)
  if (!detail) {
    return fallback
  }

  if (options.prefixFallback) {
    return `${fallback}: ${detail}`
  }

  return detail
}
