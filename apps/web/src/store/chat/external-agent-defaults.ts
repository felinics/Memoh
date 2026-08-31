import type { Ref } from 'vue'
import { getBotsByBotIdSettings } from '@memohai/sdk'
import { ACP_DEFAULT_PROJECT_MODE, ACP_DEFAULT_PROJECT_PATH } from '@/utils/acp'
import type { ExternalAgentSessionInput } from './types'

interface ExternalAgentSettings {
  default_bot_agent_id?: string | null
  chat_runtime?: string
  chat_acp_agent_id?: string | null
  chat_acp_project_path?: string | null
  chat_acp_project_mode?: string | null
}

async function fetchExternalAgentSettings(botId: string): Promise<ExternalAgentSettings | undefined> {
  // The generated SDK currently loses this response type at the public export.
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const { data } = await (getBotsByBotIdSettings as any)({
    path: { bot_id: botId },
    throwOnError: true,
  })
  return data as ExternalAgentSettings | undefined
}

export function createExternalAgentDefaults(deps: {
  currentBotId: Ref<string | null>
  sessionId: Ref<string | null>
  explicitSessionSelection: Ref<boolean>
  userScopeGeneration: () => number
  currentSelectRequest: () => number
  rememberDefault: (botId: string, input: ExternalAgentSessionInput | null) => void
  cachedDefault: (botId: string) => {
    loaded: boolean
    input: ExternalAgentSessionInput | null
  }
  pendingMatches: (input: ExternalAgentSessionInput) => boolean
  stageDefault: (input: ExternalAgentSessionInput) => void
}) {
  async function settingsForAgent(
    botId: string,
    agentId: string,
  ): Promise<Partial<ExternalAgentSessionInput>> {
    try {
      const settings = await fetchExternalAgentSettings(botId)
      const runtime = settings?.chat_runtime ?? ''
      // Direct defaults store the runtime itself in chat_runtime; the
      // acp_agent shape carries the agent id in chat_acp_agent_id.
      const matches = runtime === agentId
        || (runtime === 'acp_agent' && (settings?.chat_acp_agent_id ?? '').trim() === agentId)
      if (!settings || !matches) return {}
      return {
        botAgentId: settings.default_bot_agent_id?.trim() || undefined,
        projectPath: settings.chat_acp_project_path?.trim() || undefined,
        projectMode: settings.chat_acp_project_mode?.trim() || undefined,
      }
    } catch {
      return {}
    }
  }

  async function inputFromSettings(botId: string): Promise<ExternalAgentSessionInput | null> {
    const bid = botId.trim()
    if (!bid) return null
    const generation = deps.userScopeGeneration()
    try {
      const settings = await fetchExternalAgentSettings(bid)
      if (generation !== deps.userScopeGeneration()) return null
      if (!settings) {
        deps.rememberDefault(bid, null)
        return null
      }
      const runtime = settings?.chat_runtime ?? ''
      const isDirect = runtime === 'codex' || runtime === 'claude-code'
      if (runtime !== 'acp_agent' && !isDirect) {
        deps.rememberDefault(bid, null)
        return null
      }
      const agentId = isDirect ? runtime : (settings?.chat_acp_agent_id?.trim() ?? '')
      if (!agentId) {
        deps.rememberDefault(bid, null)
        return null
      }
      const input = {
        botAgentId: settings.default_bot_agent_id?.trim() || undefined,
        agentId,
        projectPath: settings.chat_acp_project_path?.trim()
          || ACP_DEFAULT_PROJECT_PATH,
        projectMode: settings.chat_acp_project_mode?.trim()
          || ACP_DEFAULT_PROJECT_MODE,
      }
      deps.rememberDefault(bid, input)
      return input
    } catch {
      return null
    }
  }

  async function defaultRuntimeIsExternalAgent(botId: string) {
    return await inputFromSettings(botId) !== null
  }

  async function stageFromSettings(requestId: number) {
    const botId = (deps.currentBotId.value ?? '').trim()
    if (!botId || deps.sessionId.value || deps.explicitSessionSelection.value) return
    const cached = deps.cachedDefault(botId)
    if (cached.loaded) {
      if (cached.input && !deps.pendingMatches(cached.input)) {
        deps.stageDefault(cached.input)
      }
      return
    }
    const input = await inputFromSettings(botId)
    if (!input || requestId !== deps.currentSelectRequest()) return
    if (
      (deps.currentBotId.value ?? '').trim() !== botId
      || deps.sessionId.value
      || deps.explicitSessionSelection.value
      || deps.pendingMatches(input)
    ) return
    deps.stageDefault(input)
  }

  return {
    settingsForAgent,
    defaultRuntimeIsExternalAgent,
    stageFromSettings,
  }
}
