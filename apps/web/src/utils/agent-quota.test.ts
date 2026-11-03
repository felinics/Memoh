import { describe, expect, it } from 'vitest'
import { agentQuotaNotice, agentQuotaWindowLabel } from './agent-quota'

const hourly = { used_percent: 40, window_minutes: 300, resets_at: '2026-10-01T10:00:00Z' }
const weekly = { used_percent: 40, window_minutes: 10080, resets_at: '2026-10-05T10:00:00Z' }

describe('agentQuotaNotice', () => {
  it('stays quiet below the warning threshold', () => {
    expect(agentQuotaNotice({ limit_reached: false, windows: [hourly, weekly] })).toBeNull()
  })

  it('warns about the fullest window near the limit', () => {
    const notice = agentQuotaNotice({ limit_reached: false, windows: [{ ...hourly, used_percent: 91 }, { ...weekly, used_percent: 95 }] })
    expect(notice).toEqual({ exhausted: false, window: { ...weekly, used_percent: 95 } })
  })

  it('points a reached limit at the full window that resets last', () => {
    const notice = agentQuotaNotice({ limit_reached: true, windows: [{ ...hourly, used_percent: 100 }, { ...weekly, used_percent: 100 }] })
    expect(notice).toEqual({ exhausted: true, window: { ...weekly, used_percent: 100 } })
  })
})

describe('agentQuotaWindowLabel', () => {
  const t = (key: string, params?: Record<string, unknown>) => `${key}${params ? JSON.stringify(params) : ''}`

  it('names windows by their length, and by model when they count one alone', () => {
    expect(agentQuotaWindowLabel({ window_minutes: 10080 }, t)).toBe('bots.agent.usage.window.weekly')
    expect(agentQuotaWindowLabel({ window_minutes: 300 }, t)).toBe('bots.agent.usage.window.hours{"count":5}')
    expect(agentQuotaWindowLabel({ window_minutes: 0 }, t)).toBe('bots.agent.usage.window.unknown')
    expect(agentQuotaWindowLabel({ window_minutes: 10080, model: 'Fable' }, t))
      .toBe('bots.agent.usage.window.model{"window":"bots.agent.usage.window.weekly","model":"Fable"}')
  })
})
