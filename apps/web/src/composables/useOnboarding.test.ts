// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({
  replace: vi.fn(),
  updateMe: vi.fn(),
  toastError: vi.fn(),
  user: { onboardingCompleted: false },
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({ replace: mocks.replace }),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key }),
}))

vi.mock('@memohai/sdk', () => ({
  putUsersMe: mocks.updateMe,
}))

vi.mock('@felinic/ui', () => ({
  toast: { error: mocks.toastError },
}))

vi.mock('@/store/user', () => ({
  useUserStore: () => mocks.user,
}))

const storage = new Map<string, string>()

Object.defineProperty(globalThis, 'localStorage', {
  value: {
    getItem: (key: string) => storage.get(key) ?? null,
    setItem: (key: string, value: string) => storage.set(key, value),
    removeItem: (key: string) => storage.delete(key),
    clear: () => storage.clear(),
  },
  configurable: true,
})

import { resetOnboardingState, useOnboarding } from './useOnboarding'
import {
  readOnboardingBotResult,
  resetOnboardingSession,
  writeOnboardingBotResult,
} from '@/pages/onboarding/session'
import { ONBOARDING_KEYS } from '@/pages/onboarding/constants'

describe('useOnboarding completion', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.user.onboardingCompleted = false
    mocks.updateMe.mockResolvedValue({})
    mocks.replace.mockResolvedValue(undefined)
    resetOnboardingState()
    resetOnboardingSession()
    localStorage.clear()
  })

  it('restores forced onboarding and keeps the handoff when navigation fails', async () => {
    writeOnboardingBotResult({
      botId: 'bot-id',
      modelConfigured: false,
      agent: { agentId: 'codex', botAgentId: 'agent-id' },
    })
    localStorage.setItem(ONBOARDING_KEYS.forceOnboarding, '1')

    mocks.replace.mockImplementationOnce(async () => {
      expect(localStorage.getItem(ONBOARDING_KEYS.forceOnboarding)).toBeNull()
      throw new Error('navigation failed')
    })

    expect(await useOnboarding().complete()).toBe(false)
    expect(readOnboardingBotResult()?.botId).toBe('bot-id')
    expect(localStorage.getItem(ONBOARDING_KEYS.forceOnboarding)).toBe('1')
    expect(mocks.toastError).toHaveBeenCalledWith('onboarding.complete.navigationFailed')

    mocks.replace.mockResolvedValue(undefined)
    expect(await useOnboarding().complete()).toBe(true)
    expect(mocks.replace).toHaveBeenLastCalledWith({
      name: 'bot',
      params: { botName: 'bot-id' },
      query: { agent: 'codex' },
    })
    expect(readOnboardingBotResult()).toBeNull()
    expect(localStorage.getItem(ONBOARDING_KEYS.forceOnboarding)).toBeNull()
  })

  it('treats a resolved router failure as a failed completion', async () => {
    writeOnboardingBotResult({ botId: 'bot-id', modelConfigured: true })
    mocks.replace.mockResolvedValue({ type: 4 })

    expect(await useOnboarding().complete()).toBe(false)
    expect(readOnboardingBotResult()?.botId).toBe('bot-id')
    expect(mocks.toastError).toHaveBeenCalledWith('onboarding.complete.navigationFailed')
  })
})
