const fallbackTimezones = ['UTC']

export const timezones = typeof Intl.supportedValuesOf === 'function'
  ? Intl.supportedValuesOf('timeZone')
  : fallbackTimezones

export const emptyTimezoneValue = '__empty_timezone__'

// Building a label costs one Intl.DateTimeFormat construction (~0.1ms each), so
// across ~418 zones it adds up to ~50ms. Memoise so a given zone is only
// formatted once for the whole app lifetime.
const offsetCache = new Map<string, string>()

export function getUtcOffsetLabel(tz: string): string {
  const cached = offsetCache.get(tz)
  if (cached !== undefined) return cached
  let label = ''
  try {
    const parts = new Intl.DateTimeFormat('en-US', {
      timeZone: tz,
      timeZoneName: 'shortOffset',
    }).formatToParts(new Date())
    label = parts.find(p => p.type === 'timeZoneName')?.value ?? ''
  } catch {
    label = ''
  }
  offsetCache.set(tz, label)
  return label
}

export interface TimezoneOption {
  value: string
  label: string
  description: string
}

// `description` is a getter, so constructing this list does ZERO Intl work — the
// offset for a zone is only formatted when something actually reads it, and
// getUtcOffsetLabel memoises the result. Because the dropdown is virtualized,
// opening it reads at most the ~23 visible rows' getters (~a few ms), never all
// ~418. The getter still resolves synchronously, so offsets stay searchable.
export const timezoneOptions: TimezoneOption[] = timezones.map(tz => ({
  value: tz,
  label: tz,
  get description() {
    return getUtcOffsetLabel(tz)
  },
}))

// Warm the whole cache in the background after load so even the first open (and
// the first offset search) finds everything already formatted. Runs in small
// chunks during idle time so it never blocks a frame; falls back to setTimeout
// where requestIdleCallback is unavailable (older Safari).
const scheduleIdle: (cb: () => void) => void
  = typeof window !== 'undefined' && 'requestIdleCallback' in window
    ? cb => window.requestIdleCallback(() => cb())
    : cb => setTimeout(cb, 1)

if (typeof window !== 'undefined') {
  let i = 0
  const warmChunk = () => {
    const end = Math.min(i + 40, timezones.length)
    for (; i < end; i++) getUtcOffsetLabel(timezones[i]!)
    if (i < timezones.length) scheduleIdle(warmChunk)
  }
  scheduleIdle(warmChunk)
}
