import enMessages from '@/i18n/locales/en.json'
import zhMessages from '@/i18n/locales/zh.json'
import jaMessages from '@/i18n/locales/ja.json'

interface ResolveApiErrorMessageOptions {
  prefixFallback?: boolean
}

type ErrorRecord = Record<string, unknown>
type Locale = 'en' | 'zh' | 'ja'

/** Who the server attributes a failure to, as the Problem's `fault` member. */
export type ApiErrorFault = 'client' | 'server' | 'dependency' | 'canceled'

export interface MemohError {
  code: string
  args: Record<string, unknown>
  message?: string
  requestId?: string
  traceId?: string
  status?: number
  fault?: ApiErrorFault
}

/**
 * An error raised by this client's own code whose message is already copy for
 * the user, such as a form check. resolveApiErrorMessage shows its message;
 * the text of any other error is never shown.
 */
export class UserFacingError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'UserFacingError'
  }
}

const apiErrorFaults: readonly string[] = ['client', 'server', 'dependency', 'canceled']

// The framework codes for client statuses, so an unrecognized code still gets
// the generic copy of what the client got wrong.
const clientStatusCodes: Record<number, string> = {
  400: 'http.bad_request',
  401: 'http.unauthorized',
  403: 'http.forbidden',
  404: 'http.not_found',
  405: 'http.method_not_allowed',
  409: 'http.conflict',
  413: 'http.payload_too_large',
  415: 'http.unsupported_media_type',
  426: 'http.upgrade_required',
  429: 'http.too_many_requests',
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

/**
 * The copy for an i18n key in the stored locale, for code outside a component
 * that has no vue-i18n instance. Empty when no locale has the key.
 */
export function renderI18nMessage(key: string, args?: ErrorRecord): string {
  const trimmed = key.trim()
  const template = lookupMessage(currentLocale(), trimmed) || lookupMessage('en', trimmed)
  return template ? formatMessage(template, args).trim() : ''
}

// The copy for an error is looked up by its code alone. A server-supplied
// i18n_key is not read: every code the server sends has copy under
// errors.<code>, including the rows written before the key was dropped.
function pickApiFeedbackMessage(error: unknown): string {
  for (const record of collectErrorRecords(error)) {
    const code = (typeof record.code === 'string' && record.code.trim())
      || (typeof record.error_code === 'string' && record.error_code.trim())
    if (code) {
      const rendered = renderI18nMessage(`errors.${code}`, asRecord(record.args) ?? undefined)
      if (rendered) return rendered
    }
  }
  return ''
}

function readFault(record: ErrorRecord): ApiErrorFault | undefined {
  const fault = record.fault
  return typeof fault === 'string' && apiErrorFaults.includes(fault) ? fault as ApiErrorFault : undefined
}

function readStatus(record: ErrorRecord): number | undefined {
  if (typeof record.status === 'number') return record.status
  if (typeof record.http_status === 'number') return record.http_status
  return undefined
}

// A Problem whose code this client has no copy for is described by its fault:
// what the client got wrong for a client fault, a retry for a server or
// dependency fault, and nothing for a canceled request. The Problem's detail
// is not shown. Returns undefined when the error is not a Problem with a fault.
function pickFaultMessage(error: unknown): string | undefined {
  for (const record of collectErrorRecords(error)) {
    const fault = readFault(record)
    if (!fault) continue
    if (fault === 'canceled') return ''
    if (fault === 'client') {
      const status = readStatus(record)
      const code = (status !== undefined && clientStatusCodes[status]) || 'http.bad_request'
      return renderI18nMessage(`errors.${code}`)
    }
    return renderI18nMessage('errors.internal')
  }
  return undefined
}

// An error with neither copy nor a fault is described by its HTTP status
// alone, the same way: what the client got wrong for a 4xx, a retry for a 5xx.
function pickStatusMessage(error: unknown): string {
  for (const record of collectErrorRecords(error)) {
    const status = readStatus(record)
    if (status === undefined) continue
    if (status >= 400 && status < 500) {
      return renderI18nMessage(`errors.${clientStatusCodes[status] ?? 'http.bad_request'}`)
    }
    if (status >= 500) return renderI18nMessage('errors.internal')
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

    const requestId = record.request_id ?? record.requestId
    const traceId = record.trace_id

    return {
      code,
      args: asRecord(record.args) ?? {},
      message: pickErrorDetail(record) || undefined,
      requestId: typeof requestId === 'string' && requestId.trim() ? requestId.trim() : undefined,
      traceId: typeof traceId === 'string' && traceId.trim() ? traceId.trim() : undefined,
      status: readStatus(record),
      fault: readFault(record),
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

/**
 * Whether the server answered the request with an error, so the operation it
 * asked for did not run. A request that got no answer, or whose handling the
 * server abandoned because the request was canceled, has an unknown outcome.
 */
export function isApiErrorAnswered(error: unknown): boolean {
  return apiErrorStatus(error) !== undefined && parseMemohError(error)?.fault !== 'canceled'
}

/**
 * The message to show for error. It is empty for a canceled request, which is
 * not shown. The error's own text is shown only for a UserFacingError: any
 * other error this client has no copy, fault or status for is described by
 * fallback.
 */
export function resolveApiErrorMessage(
  error: unknown,
  fallback: string,
  options: ResolveApiErrorMessageOptions = {},
): string {
  const feedback = pickApiFeedbackMessage(error)
  const faultMessage = feedback ? undefined : pickFaultMessage(error)
  if (faultMessage === '') return ''
  const detail = feedback
    || faultMessage
    || (error instanceof UserFacingError ? error.message.trim() : '')
    || pickStatusMessage(error)
    || pickNetworkErrorMessage(error)
  if (!detail) return fallback

  if (options.prefixFallback) {
    return `${fallback}: ${detail}`
  }

  return detail
}
