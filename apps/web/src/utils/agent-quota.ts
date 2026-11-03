import type { ExternalagentAgentQuotaResponse, ExternalagentAgentQuotaWindow } from '@memohai/sdk'
import { formatCalendarTime, formatRelativeTime } from './date-time'

type Translate = (key: string, params?: Record<string, unknown>) => string

const WEEK_MINUTES = 7 * 24 * 60
const DAY_MINUTES = 24 * 60
export const AGENT_QUOTA_WARNING_PERCENT = 90

export interface AgentQuotaNotice {
  exhausted: boolean
  window: ExternalagentAgentQuotaWindow
}

export function agentQuotaWindowLabel(window: Pick<ExternalagentAgentQuotaWindow, 'window_minutes' | 'model'>, t: Translate): string {
  const label = windowLengthLabel(window.window_minutes, t)
  return window.model ? t('bots.agent.usage.window.model', { window: label, model: window.model }) : label
}

function windowLengthLabel(minutes: number, t: Translate): string {
  if (minutes === WEEK_MINUTES) return t('bots.agent.usage.window.weekly')
  if (minutes > 0 && minutes % DAY_MINUTES === 0) return t('bots.agent.usage.window.days', { count: minutes / DAY_MINUTES })
  if (minutes > 0 && minutes % 60 === 0) return t('bots.agent.usage.window.hours', { count: minutes / 60 })
  if (minutes > 0) return t('bots.agent.usage.window.minutes', { count: minutes })
  return t('bots.agent.usage.window.unknown')
}

// A reset within a day reads best as a countdown; a later one needs the date.
export function agentQuotaResetTime(resetsAt: string, locale: string): string {
  const withinDay = new Date(resetsAt).getTime() - Date.now() < DAY_MINUTES * 60_000
  return withinDay ? formatRelativeTime(resetsAt, { locale }) : formatCalendarTime(resetsAt, { locale })
}

// When the account is blocked, point at the window that keeps it blocked the
// longest; otherwise warn about the fullest window once it nears the limit.
export function agentQuotaNotice(usage: ExternalagentAgentQuotaResponse | undefined): AgentQuotaNotice | null {
  const windows = usage?.windows ?? []
  if (!windows.length) return null
  if (usage?.limit_reached) {
    const full = windows.filter(window => window.used_percent >= 100)
    const candidates = full.length ? full : windows
    const window = candidates.reduce((latest, next) => resetMillis(next) > resetMillis(latest) ? next : latest)
    return { exhausted: true, window }
  }
  const fullest = windows.reduce((max, next) => next.used_percent > max.used_percent ? next : max)
  return fullest.used_percent >= AGENT_QUOTA_WARNING_PERCENT ? { exhausted: false, window: fullest } : null
}

export function agentQuotaNoticeKey(botAgentId: string, notice: AgentQuotaNotice): string {
  return `${botAgentId}:${notice.window.window_minutes}:${notice.window.resets_at ?? ''}`
}

function resetMillis(window: ExternalagentAgentQuotaWindow): number {
  return window.resets_at ? new Date(window.resets_at).getTime() : 0
}
