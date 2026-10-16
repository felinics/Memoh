import { describe, expect, it } from 'vitest'
import { codexUsageNotice, codexUsageWindowLabel } from './codex-usage'

const hourly = { used_percent: 40, window_minutes: 300, resets_at: '2026-10-01T10:00:00Z' }
const weekly = { used_percent: 40, window_minutes: 10080, resets_at: '2026-10-05T10:00:00Z' }

describe('codexUsageNotice', () => {
  it('stays quiet below the warning threshold', () => {
    expect(codexUsageNotice({ limit_reached: false, windows: [hourly, weekly] })).toBeNull()
  })

  it('warns about the fullest window near the limit', () => {
    const notice = codexUsageNotice({ limit_reached: false, windows: [{ ...hourly, used_percent: 91 }, { ...weekly, used_percent: 95 }] })
    expect(notice).toEqual({ exhausted: false, window: { ...weekly, used_percent: 95 } })
  })

  it('points a reached limit at the full window that resets last', () => {
    const notice = codexUsageNotice({ limit_reached: true, windows: [{ ...hourly, used_percent: 100 }, { ...weekly, used_percent: 100 }] })
    expect(notice).toEqual({ exhausted: true, window: { ...weekly, used_percent: 100 } })
  })
})

describe('codexUsageWindowLabel', () => {
  const t = (key: string, params?: Record<string, unknown>) => `${key}${params ? JSON.stringify(params) : ''}`

  it('names windows by their length', () => {
    expect(codexUsageWindowLabel(10080, t)).toBe('bots.agent.usage.window.weekly')
    expect(codexUsageWindowLabel(300, t)).toBe('bots.agent.usage.window.hours{"count":5}')
    expect(codexUsageWindowLabel(0, t)).toBe('bots.agent.usage.window.unknown')
  })
})
