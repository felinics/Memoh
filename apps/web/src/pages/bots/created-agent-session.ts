import { safeSessionGet, safeSessionRemove, safeSessionSet } from '@/utils/safe-storage'
import type { BotCreateSettings } from '@/store/bot-create-progress'

const KEY = 'memoh:new-bot:authorization'
export interface CreatedAgentSession {
  botId: string
  botName: string
  displayName: string
  authorizationId?: string
  agentId: string
  runtime: 'codex' | 'claude-code'
  setupError: string | null
  settings?: BotCreateSettings
}

// Keep only the created target, never credentials or a device login session.
// Refresh must resume authorization rather than send the user to Create again.
export function readCreatedAgentSession(onboarding = false): CreatedAgentSession | null {
  try {
    const value = JSON.parse(safeSessionGet(onboarding ? 'memoh:onboarding:creation' : KEY) || 'null') as Partial<CreatedAgentSession> | null
    if (!value || typeof value.botId !== 'string' || !value.botId.trim()
      || typeof value.agentId !== 'string'
      || (value.runtime !== 'codex' && value.runtime !== 'claude-code')) return null
    return {
      botId: value.botId,
      agentId: value.agentId,
      authorizationId: typeof value.authorizationId === 'string' ? value.authorizationId : undefined,
      runtime: value.runtime,
      botName: typeof value.botName === 'string' ? value.botName : '',
      displayName: typeof value.displayName === 'string' ? value.displayName : '',
      setupError: typeof value.setupError === 'string' ? value.setupError : null,
      settings: value.settings && {
        chat_model_id: typeof value.settings.chat_model_id === 'string' ? value.settings.chat_model_id : undefined,
        memory_provider_id: typeof value.settings.memory_provider_id === 'string' ? value.settings.memory_provider_id : undefined,
        reasoning_effort: typeof value.settings.reasoning_effort === 'string' ? value.settings.reasoning_effort : undefined,
      },
    }
  } catch {
    return null
  }
}

export function writeCreatedAgentSession(value: CreatedAgentSession | null, onboarding = false) {
  const key = onboarding ? 'memoh:onboarding:creation' : KEY
  if (value) safeSessionSet(key, JSON.stringify(value))
  else safeSessionRemove(key)
}
