// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ONBOARDING_KEYS } from './constants'

describe('onboarding session handoff', () => {
  beforeEach(() => {
    vi.resetModules()
    sessionStorage.clear()
  })

  it('keeps provider and bot handoffs independent', async () => {
    const session = await import('./session')
    session.writeOnboardingProviderId('provider-id')
    session.writeOnboardingBotResult({
      botId: 'bot-id',
      modelConfigured: true,
    })

    expect(session.readOnboardingProviderId()).toBe('provider-id')
    expect(session.readOnboardingBotResult()).toEqual({
      botId: 'bot-id',
      modelConfigured: true,
    })
  })

  it('persists only the non-sensitive bot handoff schema', async () => {
    const session = await import('./session')
    session.writeOnboardingBotResult({
      botId: 'bot-id',
      modelConfigured: false,
      agent: {
        agentId: 'Codex',
        botAgentId: 'agent-id',
      },
      managed: { api_key: 'must-not-be-stored' },
    } as Parameters<typeof session.writeOnboardingBotResult>[0] & {
      managed: Record<string, string>
    })

    const raw = sessionStorage.getItem(ONBOARDING_KEYS.botResult)
    expect(raw).not.toContain('must-not-be-stored')
    expect(JSON.parse(raw!)).toEqual({
      botId: 'bot-id',
      modelConfigured: false,
      agent: { agentId: 'codex', botAgentId: 'agent-id' },
    })
  })

  it('prefers the in-memory handoff when storage cannot be updated', async () => {
    sessionStorage.setItem(ONBOARDING_KEYS.providerId, 'old-provider')
    sessionStorage.setItem(ONBOARDING_KEYS.botResult, JSON.stringify({
      botId: 'old-bot',
      modelConfigured: false,
    }))
    const session = await import('./session')
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    const setItem = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new DOMException('blocked', 'SecurityError')
    })

    session.writeOnboardingProviderId('new-provider')
    session.writeOnboardingBotResult({ botId: 'new-bot', modelConfigured: true })

    expect(session.readOnboardingProviderId()).toBe('new-provider')
    expect(session.readOnboardingBotResult()?.botId).toBe('new-bot')
    setItem.mockRestore()
    warn.mockRestore()
  })

  it('rejects malformed storage and normalizes agent identifiers', async () => {
    sessionStorage.setItem(ONBOARDING_KEYS.providerId, '   ')
    sessionStorage.setItem(ONBOARDING_KEYS.botResult, JSON.stringify({
      botId: 'bot-id',
      modelConfigured: true,
      agent: { agentId: ' CODEX ', botAgentId: ' agent-id ' },
    }))

    const session = await import('./session')
    expect(session.readOnboardingProviderId()).toBe('')
    expect(session.readOnboardingBotResult()).toEqual({
      botId: 'bot-id',
      modelConfigured: true,
      agent: { agentId: 'codex', botAgentId: 'agent-id' },
    })

    sessionStorage.setItem(ONBOARDING_KEYS.botResult, '{broken')
    expect(session.readOnboardingBotResult()).toBeNull()
  })

  it('clears both handoffs and purges legacy state that could contain credentials', async () => {
    sessionStorage.setItem(ONBOARDING_KEYS.providerId, 'provider-id')
    sessionStorage.setItem(ONBOARDING_KEYS.botResult, JSON.stringify({
      botId: 'bot-id',
      modelConfigured: true,
    }))
    sessionStorage.setItem('memoh:onboarding:runtime-state', JSON.stringify({
      selection: { kind: 'acp', selection: { managed: { api_key: 'secret' } } },
    }))
    sessionStorage.setItem('memoh:onboarding:acp-selection', JSON.stringify({
      agentId: 'codex',
      setupMode: 'api_key',
      managed: { api_key: 'secret' },
    }))

    const session = await import('./session')
    expect(sessionStorage.getItem('memoh:onboarding:runtime-state')).toBeNull()
    expect(sessionStorage.getItem('memoh:onboarding:acp-selection')).toBeNull()

    sessionStorage.setItem('memoh:onboarding:runtime-state', 'stale-again')
    sessionStorage.setItem('memoh:onboarding:acp-selection', 'stale-again')
    session.resetOnboardingSession()

    expect(session.readOnboardingProviderId()).toBe('')
    expect(session.readOnboardingBotResult()).toBeNull()
    expect(sessionStorage.getItem(ONBOARDING_KEYS.providerId)).toBeNull()
    expect(sessionStorage.getItem(ONBOARDING_KEYS.botResult)).toBeNull()
    expect(sessionStorage.getItem('memoh:onboarding:runtime-state')).toBeNull()
    expect(sessionStorage.getItem('memoh:onboarding:acp-selection')).toBeNull()
  })
})
