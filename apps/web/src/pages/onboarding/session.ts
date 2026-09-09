import { normalizeACPAgentID } from '@/utils/acp'
import { safeSessionGet, safeSessionRemove, safeSessionSet } from '@/utils/safe-storage'
import { ONBOARDING_KEYS } from './constants'

const LEGACY_SESSION_KEYS = [
  'memoh:onboarding:runtime-state',
  'memoh:onboarding:acp-selection',
  'memoh:onboarding:created-bot-id',
  'memoh:onboarding:provider-added-count',
] as const

export interface OnboardingAgentResult {
  agentId: string
  botAgentId: string
}

export interface OnboardingBotResult {
  botId: string
  modelConfigured: boolean
  agent?: OnboardingAgentResult
}

let providerIdMemory: string | undefined
let botResultMemory: OnboardingBotResult | null | undefined

function normalizeProviderId(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function normalizeBotResult(value: unknown): OnboardingBotResult | null {
  if (!value || typeof value !== 'object') return null
  const candidate = value as Partial<OnboardingBotResult>
  const botId = normalizeProviderId(candidate.botId)
  if (!botId) return null

  const legacy = candidate as Partial<OnboardingBotResult> & { acp?: OnboardingAgentResult }
  const selectedAgent = candidate.agent ?? legacy.acp
  const agentId = normalizeACPAgentID(selectedAgent?.agentId)
  const botAgentId = normalizeProviderId(selectedAgent?.botAgentId)

  return {
    botId,
    modelConfigured: candidate.modelConfigured === true,
    ...(agentId && botAgentId && {
      agent: {
        agentId,
        botAgentId,
      },
    }),
  }
}

export function readOnboardingProviderId(): string {
  if (providerIdMemory !== undefined) return providerIdMemory
  return normalizeProviderId(safeSessionGet(ONBOARDING_KEYS.providerId))
}

export function writeOnboardingProviderId(providerId: string): void {
  providerIdMemory = normalizeProviderId(providerId)
  if (providerIdMemory) {
    safeSessionSet(ONBOARDING_KEYS.providerId, providerIdMemory)
  } else {
    safeSessionRemove(ONBOARDING_KEYS.providerId)
  }
}

export function readOnboardingBotResult(): OnboardingBotResult | null {
  if (botResultMemory !== undefined) return botResultMemory
  const raw = safeSessionGet(ONBOARDING_KEYS.botResult)
  if (raw) {
    try {
      return normalizeBotResult(JSON.parse(raw))
    } catch {
      return null
    }
  }
  return null
}

export function writeOnboardingBotResult(result: OnboardingBotResult): void {
  botResultMemory = normalizeBotResult(result)
  if (!botResultMemory) {
    clearOnboardingBotResult()
    return
  }
  safeSessionSet(
    ONBOARDING_KEYS.botResult,
    JSON.stringify(botResultMemory),
  )
}

export function clearOnboardingBotResult(): void {
  botResultMemory = null
  safeSessionRemove(ONBOARDING_KEYS.botResult)
}

export function resetOnboardingSession(): void {
  providerIdMemory = ''
  botResultMemory = null
  safeSessionRemove(ONBOARDING_KEYS.providerId)
  safeSessionRemove(ONBOARDING_KEYS.botResult)
  purgeLegacyOnboardingSession()
}

function purgeLegacyOnboardingSession(): void {
  for (const key of LEGACY_SESSION_KEYS) safeSessionRemove(key)
}

// Older onboarding state could include ACP API keys. It has no valid consumer
// after this module replaces those shapes, so purge it eagerly.
purgeLegacyOnboardingSession()
