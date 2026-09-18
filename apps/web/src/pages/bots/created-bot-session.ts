import { safeSessionGet, safeSessionRemove, safeSessionSet } from '@/utils/safe-storage'
import type { BotCreateSettings } from '@/store/bot-create-progress'

const KEY = 'memoh:new-bot:authorization'
const ONBOARDING_KEY = 'memoh:onboarding:creation'

export type CreatedBotSessionRuntime = 'codex' | 'claude-code'

// The Bot a create flow already produced. The bot row exists on the server as
// soon as `bot_created` streams, so a refresh must resume on that Bot instead of
// posting a second create (which would take another Bot slot or collide on the
// name). Agent fields are only present for the direct Codex / Claude Code flows.
export interface CreatedBotSession {
  botId: string
  botName: string
  displayName: string
  avatarUrl?: string
  authorizationId?: string
  agentId?: string
  runtime?: CreatedBotSessionRuntime
  setupError: string | null
  settings?: BotCreateSettings
}

function isRuntime(value: unknown): value is CreatedBotSessionRuntime {
  return value === 'codex' || value === 'claude-code'
}

function optionalString(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value : undefined
}

// Keep only the created target, never credentials or a device login session.
export function readCreatedBotSession(onboarding = false): CreatedBotSession | null {
  try {
    const value = JSON.parse(safeSessionGet(onboarding ? ONBOARDING_KEY : KEY) || 'null') as Partial<CreatedBotSession> | null
    if (!value || typeof value.botId !== 'string' || !value.botId.trim()) return null
    // A saved Agent must name a supported direct runtime; anything else is a
    // stale shape from an older client and is dropped as a whole.
    if (value.runtime !== undefined && !isRuntime(value.runtime)) return null
    if (value.agentId !== undefined && typeof value.agentId !== 'string') return null
    return {
      botId: value.botId,
      botName: typeof value.botName === 'string' ? value.botName : '',
      displayName: typeof value.displayName === 'string' ? value.displayName : '',
      ...(optionalString(value.avatarUrl) && { avatarUrl: value.avatarUrl }),
      ...(optionalString(value.authorizationId) && { authorizationId: value.authorizationId }),
      ...(value.agentId !== undefined && { agentId: value.agentId }),
      ...(value.runtime !== undefined && { runtime: value.runtime }),
      setupError: typeof value.setupError === 'string' ? value.setupError : null,
      ...(value.settings && { settings: {
        chat_model_id: optionalString(value.settings.chat_model_id),
        memory_provider_id: optionalString(value.settings.memory_provider_id),
        reasoning_effort: optionalString(value.settings.reasoning_effort),
      } }),
    }
  } catch {
    return null
  }
}

export function writeCreatedBotSession(value: CreatedBotSession | null, onboarding = false) {
  const key = onboarding ? ONBOARDING_KEY : KEY
  if (value) safeSessionSet(key, JSON.stringify(value))
  else safeSessionRemove(key)
}
