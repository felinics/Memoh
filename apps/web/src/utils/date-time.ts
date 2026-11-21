interface FormatDateOptions {
  fallback?: string
  invalidFallback?: string
  locale?: string
}

type DateResolution =
  | { kind: 'missing' }
  | { kind: 'invalid' }
  | { kind: 'valid'; date: Date }

function isGoZeroDate(date: Date): boolean {
  // Go serializes zero time.Time{} values as year 0001 (e.g. "0001-01-01T00:00:00Z").
  // Treat them as unset so callers render their empty fallback instead of "1/1/1".
  return date.getUTCFullYear() <= 1
}

function resolveDate(value: string | Date | null | undefined): DateResolution {
  if (!value) return { kind: 'missing' }
  const parsed = value instanceof Date ? value : new Date(value)
  if (Number.isNaN(parsed.getTime())) return { kind: 'invalid' }
  if (isGoZeroDate(parsed)) return { kind: 'missing' }
  return { kind: 'valid', date: parsed }
}

/**
 * Resolves the display string when a date value is non-null but could not be
 * parsed.  `invalidFallback` takes precedence over `fallback`; when neither is
 * supplied the raw input value is returned so callers can see what arrived.
 */
function resolveInvalid(value: string, options: FormatDateOptions): string {
  if (options.invalidFallback !== undefined) return options.invalidFallback
  if (options.fallback !== undefined) return options.fallback
  return value
}

export function formatDateTime(
  value: string | null | undefined,
  options: FormatDateOptions = {},
): string {
  const resolved = resolveDate(value)
  if (resolved.kind === 'missing') return options.fallback ?? ''
  if (resolved.kind === 'invalid') return resolveInvalid(value ?? '', options)
  return resolved.date.toLocaleString(options.locale)
}

export function formatDate(
  value: string | null | undefined,
  options: FormatDateOptions = {},
): string {
  const resolved = resolveDate(value)
  if (resolved.kind === 'missing') return options.fallback ?? ''
  if (resolved.kind === 'invalid') return resolveInvalid(value ?? '', options)
  return resolved.date.toLocaleDateString(options.locale)
}

export function formatDateTimeSeconds(
  value: string | null | undefined,
  options: FormatDateOptions = {},
): string {
  const resolved = resolveDate(value)
  if (resolved.kind === 'missing') return options.fallback ?? ''
  if (resolved.kind === 'invalid') return resolveInvalid(value ?? '', options)
  const parsed = resolved.date

  const year = parsed.getFullYear()
  const month = String(parsed.getMonth() + 1).padStart(2, '0')
  const day = String(parsed.getDate()).padStart(2, '0')
  const hours = String(parsed.getHours()).padStart(2, '0')
  const minutes = String(parsed.getMinutes()).padStart(2, '0')
  const seconds = String(parsed.getSeconds()).padStart(2, '0')
  return `${year}-${month}-${day} ${hours}:${minutes}:${seconds}`
}

/**
 * Compact, locale-aware "month day, time" stamp for dense data tables — e.g.
 * "Jun 11, 6:43 PM" (en) or "6月11日 下午6:43" (zh). Year is omitted (usage logs
 * are recent), and seconds are dropped so rows read at a glance instead of as a
 * full ISO timestamp.
 */
export function formatDateTimeShort(
  value: string | null | undefined,
  options: FormatDateOptions = {},
): string {
  const resolved = resolveDate(value)
  if (resolved.kind === 'missing') return options.fallback ?? ''
  if (resolved.kind === 'invalid') return resolveInvalid(value ?? '', options)
  return resolved.date.toLocaleString(options.locale, {
    month: 'short',
    day: 'numeric',
    hour: 'numeric',
    minute: '2-digit',
    hour12: true,
  })
}

/**
 * Calendar-anchored stamp for a single item's detail (e.g. a chat turn's "more"
 * menu): "Today 10:11 PM" / "Yesterday 10:11 PM" for the last two days, then
 * "Jun 14, 10:11 PM" within the year and "Jun 14, 2025, 10:11 PM" beyond it.
 * Unlike `formatRelativeTime` it never decays to a vague "3 hours ago" — the
 * point here is a precise, readable time the reader can trust at a glance.
 * Today/Yesterday are produced via `Intl.RelativeTimeFormat` so the words are
 * localized (zh → 今天 / 昨天) without a translation table.
 * `hour12: false` switches to a zero-padded 24-hour clock ("Today 08:00"), for
 * surfaces that already show times that way, such as the schedule panel.
 */
export function formatCalendarTime(
  value: string | null | undefined,
  options: FormatDateOptions & { hour12?: boolean } = {},
): string {
  const resolved = resolveDate(value)
  if (resolved.kind === 'missing') return options.fallback ?? ''
  if (resolved.kind === 'invalid') return resolveInvalid(value ?? '', options)
  const parsed = resolved.date

  const hour12 = options.hour12 ?? true
  const clock: Intl.DateTimeFormatOptions = {
    hour: hour12 ? 'numeric' : '2-digit',
    minute: '2-digit',
    hour12,
  }
  const time = parsed.toLocaleTimeString(options.locale, clock)

  const startOfDay = (d: Date) => new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime()
  const dayDiff = Math.round((startOfDay(parsed) - startOfDay(new Date())) / 86_400_000)

  if (dayDiff === 0 || dayDiff === -1) {
    const rtf = new Intl.RelativeTimeFormat(options.locale, { numeric: 'auto' })
    const day = rtf.format(dayDiff, 'day')
    return `${day.charAt(0).toUpperCase()}${day.slice(1)} ${time}`
  }

  const sameYear = parsed.getFullYear() === new Date().getFullYear()
  return parsed.toLocaleString(options.locale, {
    year: sameYear ? undefined : 'numeric',
    month: 'short',
    day: 'numeric',
    ...clock,
  })
}

/**
 * Returns a locale-aware relative time string such as "3 minutes ago" or
 * "in 2 days".  Falls back to `toLocaleDateString()` for dates older than a
 * week.  Accepts either an ISO string or a `Date` object.
 *
 * Uses `Intl.RelativeTimeFormat` so callers can align the output language with
 * the app locale instead of relying on the browser's preferred locale.
 */
export function formatRelativeTime(
  value: string | Date | null | undefined,
  options: FormatDateOptions = {},
): string {
  const resolved = resolveDate(value)
  if (resolved.kind === 'missing') return options.fallback ?? ''
  if (resolved.kind === 'invalid') return resolveInvalid(String(value ?? ''), options)
  const date = resolved.date

  const diffMs = date.getTime() - Date.now()
  const absDiffSec = Math.abs(diffMs) / 1000
  const rtf = new Intl.RelativeTimeFormat(options.locale, { numeric: 'auto' })

  if (absDiffSec < 60) return rtf.format(Math.round(diffMs / 1000), 'second')
  if (absDiffSec < 3_600) return rtf.format(Math.round(diffMs / 60_000), 'minute')
  if (absDiffSec < 86_400) return rtf.format(Math.round(diffMs / 3_600_000), 'hour')
  if (absDiffSec < 604_800) return rtf.format(Math.round(diffMs / 86_400_000), 'day')

  // Beyond a week: absolute date is more readable than "34 days ago"
  return date.toLocaleDateString(options.locale)
}
