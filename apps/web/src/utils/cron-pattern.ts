import cronstrue from 'cronstrue'
import 'cronstrue/locales/zh_CN'
import 'cronstrue/locales/ja'
import { CronExpressionParser } from 'cron-parser'

export type ScheduleMode =
  | 'minutes'
  | 'hourly'
  | 'daily'
  | 'weekly'
  | 'monthly'
  | 'yearly'
  | 'advanced'

export interface ScheduleFormState {
  mode: ScheduleMode
  intervalMinutes: number
  minute: number
  hours: number[]
  weekdays: number[]
  monthDays: number[]
  month: number
  monthDay: number
  advancedPattern: string
}

// Default form state used when creating a new schedule. Chosen so that a user
// who simply clicks "create" and hits save gets a reasonable daily-at-09:00
// pattern.
export function defaultScheduleFormState(): ScheduleFormState {
  return {
    mode: 'daily',
    intervalMinutes: 30,
    minute: 0,
    hours: [9],
    weekdays: [1, 2, 3, 4, 5],
    monthDays: [1],
    month: 1,
    monthDay: 1,
    advancedPattern: '',
  }
}

function assertInt(value: number, min: number, max: number, label: string) {
  if (!Number.isInteger(value) || value < min || value > max) {
    throw new Error(`${label} must be an integer in [${min}, ${max}], got ${value}`)
  }
}

function dedupSort(values: number[]): number[] {
  return Array.from(new Set(values)).sort((a, b) => a - b)
}

function formatList(values: number[]): string {
  const normalized = dedupSort(values)
  if (normalized.length === 0) throw new Error('list cannot be empty')
  return normalized.join(',')
}

// Produce a canonical 5-field cron pattern from the form state. Always returns
// the standard `minute hour dom month dow` shape (never seconds, never
// descriptors) so that fromCron can always recognise outputs of toCron and
// round-trip to the same state.
export function toCron(state: ScheduleFormState): string {
  switch (state.mode) {
    case 'minutes': {
      assertInt(state.intervalMinutes, 1, 59, 'intervalMinutes')
      return `*/${state.intervalMinutes} * * * *`
    }
    case 'hourly': {
      assertInt(state.minute, 0, 59, 'minute')
      return `${state.minute} * * * *`
    }
    case 'daily': {
      assertInt(state.minute, 0, 59, 'minute')
      state.hours.forEach(h => assertInt(h, 0, 23, 'hour'))
      const hourField = formatList(state.hours)
      return `${state.minute} ${hourField} * * *`
    }
    case 'weekly': {
      assertInt(state.minute, 0, 59, 'minute')
      if (state.hours.length !== 1) throw new Error('weekly mode requires a single hour')
      assertInt(state.hours[0]!, 0, 23, 'hour')
      state.weekdays.forEach(d => assertInt(d, 0, 6, 'weekday'))
      const dowField = formatList(state.weekdays)
      return `${state.minute} ${state.hours[0]} * * ${dowField}`
    }
    case 'monthly': {
      assertInt(state.minute, 0, 59, 'minute')
      if (state.hours.length !== 1) throw new Error('monthly mode requires a single hour')
      assertInt(state.hours[0]!, 0, 23, 'hour')
      state.monthDays.forEach(d => assertInt(d, 1, 31, 'monthDay'))
      const domField = formatList(state.monthDays)
      return `${state.minute} ${state.hours[0]} ${domField} * *`
    }
    case 'yearly': {
      assertInt(state.minute, 0, 59, 'minute')
      if (state.hours.length !== 1) throw new Error('yearly mode requires a single hour')
      assertInt(state.hours[0]!, 0, 23, 'hour')
      assertInt(state.month, 1, 12, 'month')
      assertInt(state.monthDay, 1, 31, 'monthDay')
      return `${state.minute} ${state.hours[0]} ${state.monthDay} ${state.month} *`
    }
    case 'advanced':
      return state.advancedPattern.trim()
  }
}

// --- fromCron helpers ---------------------------------------------------------

// Strictly match "*", returning true. No range/step tolerance — we want
// lossless round-trips only.
function isStar(field: string): boolean {
  return field === '*'
}

// Match a single non-negative integer. Returns undefined if not a plain int.
function parseIntField(field: string): number | undefined {
  if (!/^\d+$/.test(field)) return undefined
  return Number(field)
}

// Match a plain integer list "a,b,c" (no ranges, no steps). Returns sorted
// unique numbers, or undefined on any non-conforming input.
function parseIntList(field: string): number[] | undefined {
  if (field === '') return undefined
  const parts = field.split(',')
  const out: number[] = []
  for (const part of parts) {
    const n = parseIntField(part)
    if (n === undefined) return undefined
    out.push(n)
  }
  return dedupSort(out)
}

function parseStep(field: string): number | undefined {
  const m = /^\*\/(\d+)$/.exec(field)
  if (!m) return undefined
  const n = Number(m[1])
  return Number.isInteger(n) ? n : undefined
}

function inRange(values: number[], min: number, max: number): boolean {
  return values.every(v => v >= min && v <= max)
}

// Parse a stored pattern back into form state. Any pattern that toCron could
// not have produced (descriptors, 6-field seconds cron, ranges, steps other
// than the minutes mode, named day-of-week tokens, etc.) falls back to
// 'advanced' with the raw text preserved. This is intentional — lossy
// recognition would let the builder UI silently rewrite the AI-generated
// pattern on edit.
export function fromCron(pattern: string): ScheduleFormState {
  const base = defaultScheduleFormState()
  const raw = pattern.trim()
  const advanced: ScheduleFormState = { ...base, mode: 'advanced', advancedPattern: raw }
  if (!raw) return advanced

  // Descriptors (@daily, @every 1h, ...) and seconds cron have 1 or 6
  // space-separated tokens respectively; only 5-field standard cron maps to
  // structured modes.
  const fields = raw.split(/\s+/)
  if (fields.length !== 5) return advanced

  const [minuteF, hourF, domF, monthF, dowF] = fields as [string, string, string, string, string]

  // minutes:  */N  *  *  *  *
  {
    const step = parseStep(minuteF)
    if (step !== undefined && isStar(hourF) && isStar(domF) && isStar(monthF) && isStar(dowF)) {
      if (step >= 1 && step <= 59) {
        return { ...base, mode: 'minutes', intervalMinutes: step }
      }
    }
  }

  // hourly:  M  *  *  *  *
  {
    const m = parseIntField(minuteF)
    if (m !== undefined && isStar(hourF) && isStar(domF) && isStar(monthF) && isStar(dowF)) {
      if (m >= 0 && m <= 59) {
        return { ...base, mode: 'hourly', minute: m }
      }
    }
  }

  // daily:  M  H[,H]  *  *  *
  {
    const m = parseIntField(minuteF)
    const hours = parseIntList(hourF)
    if (
      m !== undefined && m >= 0 && m <= 59
      && hours && hours.length > 0 && inRange(hours, 0, 23)
      && isStar(domF) && isStar(monthF) && isStar(dowF)
    ) {
      return { ...base, mode: 'daily', minute: m, hours }
    }
  }

  // weekly:  M  H  *  *  DOW[,DOW]
  {
    const m = parseIntField(minuteF)
    const h = parseIntField(hourF)
    const weekdays = parseIntList(dowF)
    if (
      m !== undefined && m >= 0 && m <= 59
      && h !== undefined && h >= 0 && h <= 23
      && isStar(domF) && isStar(monthF)
      && weekdays && weekdays.length > 0 && inRange(weekdays, 0, 6)
    ) {
      return { ...base, mode: 'weekly', minute: m, hours: [h], weekdays }
    }
  }

  // monthly:  M  H  D[,D]  *  *
  {
    const m = parseIntField(minuteF)
    const h = parseIntField(hourF)
    const monthDays = parseIntList(domF)
    if (
      m !== undefined && m >= 0 && m <= 59
      && h !== undefined && h >= 0 && h <= 23
      && monthDays && monthDays.length > 0 && inRange(monthDays, 1, 31)
      && isStar(monthF) && isStar(dowF)
    ) {
      return { ...base, mode: 'monthly', minute: m, hours: [h], monthDays }
    }
  }

  // yearly:  M  H  D  Mo  *
  {
    const m = parseIntField(minuteF)
    const h = parseIntField(hourF)
    const d = parseIntField(domF)
    const mo = parseIntField(monthF)
    if (
      m !== undefined && m >= 0 && m <= 59
      && h !== undefined && h >= 0 && h <= 23
      && d !== undefined && d >= 1 && d <= 31
      && mo !== undefined && mo >= 1 && mo <= 12
      && isStar(dowF)
    ) {
      return { ...base, mode: 'yearly', minute: m, hours: [h], month: mo, monthDay: d }
    }
  }

  return advanced
}

// --- preview helpers ---------------------------------------------------------

export type CronLocale = 'en' | 'zh' | 'ja'

// Returns a localized human-readable description of the cron pattern, or
// undefined if cronstrue cannot parse it.
export function describeCron(pattern: string, locale: CronLocale): string | undefined {
  const trimmed = pattern.trim()
  if (!trimmed) return undefined
  try {
    return cronstrue.toString(trimmed, {
      locale: locale === 'zh' ? 'zh_CN' : locale,
      use24HourTimeFormat: true,
      throwExceptionOnParseError: true,
      verbose: false,
    })
  } catch {
    return undefined
  }
}

// Returns the next `count` trigger dates for the given pattern, evaluated in
// the provided IANA timezone. Returns an empty array on parse failure.
export function nextRuns(pattern: string, timezone: string | undefined, count: number): Date[] {
  const trimmed = pattern.trim()
  if (!trimmed) return []
  try {
    const tz = timezone && timezone.trim() !== '' ? timezone : undefined
    const iter = CronExpressionParser.parse(trimmed, tz ? { tz } : {})
    const out: Date[] = []
    for (let i = 0; i < count; i++) {
      const d = iter.next().toDate()
      out.push(d)
    }
    return out
  } catch {
    return []
  }
}

// Returns true iff `pattern` can be parsed by cron-parser. Used to guard
// submission of the 'advanced' mode.
export function isValidCron(pattern: string): boolean {
  const trimmed = pattern.trim()
  if (!trimmed) return false
  try {
    CronExpressionParser.parse(trimmed)
    return true
  } catch {
    return false
  }
}

// Localized weekday labels. 0 = Sunday per ISO cron convention.
export const WEEKDAY_KEYS = [
  'sun', 'mon', 'tue', 'wed', 'thu', 'fri', 'sat',
] as const
