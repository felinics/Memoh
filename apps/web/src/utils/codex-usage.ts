import type { ExternalagentCodexUsageResponse, ExternalagentCodexUsageWindow } from '@memohai/sdk'
import { formatCalendarTime, formatRelativeTime } from './date-time'

type Translate = (key: string, params?: Record<string, unknown>) => string

const WEEK_MINUTES = 7 * 24 * 60
const DAY_MINUTES = 24 * 60
export const CODEX_USAGE_WARNING_PERCENT = 90

export interface CodexUsageNotice {
  exhausted: boolean
  window: ExternalagentCodexUsageWindow
}

export function codexUsageWindowLabel(minutes: number, t: Translate): string {
  if (minutes === WEEK_MINUTES) return t('bots.agent.usage.window.weekly')
  if (minutes > 0 && minutes % DAY_MINUTES === 0) return t('bots.agent.usage.window.days', { count: minutes / DAY_MINUTES })
  if (minutes > 0 && minutes % 60 === 0) return t('bots.agent.usage.window.hours', { count: minutes / 60 })
  if (minutes > 0) return t('bots.agent.usage.window.minutes', { count: minutes })
  return t('bots.agent.usage.window.unknown')
}

// A reset within a day reads best as a countdown; a later one needs the date.
export function codexUsageResetTime(resetsAt: string, locale: string): string {
  const withinDay = new Date(resetsAt).getTime() - Date.now() < DAY_MINUTES * 60_000
  return withinDay ? formatRelativeTime(resetsAt, { locale }) : formatCalendarTime(resetsAt, { locale })
}

// When the account is blocked, point at the window that keeps it blocked the
// longest; otherwise warn about the fullest window once it nears the limit.
export function codexUsageNotice(usage: ExternalagentCodexUsageResponse | undefined): CodexUsageNotice | null {
  const windows = usage?.windows ?? []
  if (!windows.length) return null
  if (usage?.limit_reached) {
    const full = windows.filter(window => window.used_percent >= 100)
    const candidates = full.length ? full : windows
    const window = candidates.reduce((latest, next) => resetMillis(next) > resetMillis(latest) ? next : latest)
    return { exhausted: true, window }
  }
  const fullest = windows.reduce((max, next) => next.used_percent > max.used_percent ? next : max)
  return fullest.used_percent >= CODEX_USAGE_WARNING_PERCENT ? { exhausted: false, window: fullest } : null
}

export function codexUsageNoticeKey(botAgentId: string, notice: CodexUsageNotice): string {
  return `${botAgentId}:${notice.window.window_minutes}:${notice.window.resets_at ?? ''}`
}

function resetMillis(window: ExternalagentCodexUsageWindow): number {
  return window.resets_at ? new Date(window.resets_at).getTime() : 0
}
