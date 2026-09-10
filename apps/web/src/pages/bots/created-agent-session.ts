import { safeSessionGet, safeSessionRemove, safeSessionSet } from '@/utils/safe-storage'

const KEY = 'memoh:new-bot:authorization'
export interface CreatedAgentSession {
  botId: string
  botName: string
  displayName: string
  agentId: string
  runtime: 'codex' | 'claude-code'
  setupError: string | null
}

// Keep only the created target, never credentials or a device login session.
// Refresh must resume authorization rather than send the user to Create again.
export function readCreatedAgentSession(): CreatedAgentSession | null {
  try {
    const value = JSON.parse(safeSessionGet(KEY) || 'null') as Partial<CreatedAgentSession> | null
    if (!value || typeof value.botId !== 'string' || !value.botId.trim()
      || typeof value.agentId !== 'string'
      || (value.runtime !== 'codex' && value.runtime !== 'claude-code')) return null
    return {
      botId: value.botId,
      agentId: value.agentId,
      runtime: value.runtime,
      botName: typeof value.botName === 'string' ? value.botName : '',
      displayName: typeof value.displayName === 'string' ? value.displayName : '',
      setupError: typeof value.setupError === 'string' ? value.setupError : null,
    }
  } catch {
    return null
  }
}

export function writeCreatedAgentSession(value: CreatedAgentSession | null) {
  if (value) safeSessionSet(KEY, JSON.stringify(value))
  else safeSessionRemove(KEY)
}
