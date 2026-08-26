import type { Ref } from 'vue'
import { getBotsByBotIdSettings } from '@memohai/sdk'
import { ACP_DEFAULT_PROJECT_MODE, ACP_DEFAULT_PROJECT_PATH } from '@/utils/acp'
import type { ACPAgentSessionInput } from './types'

interface ACPSettings {
  default_bot_agent_id?: string | null
  chat_runtime?: string
  chat_acp_agent_id?: string | null
  chat_acp_project_path?: string | null
  chat_acp_project_mode?: string | null
}

async function fetchACPSettings(botId: string): Promise<ACPSettings | undefined> {
  // The generated SDK currently loses this response type at the public export.
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const { data } = await (getBotsByBotIdSettings as any)({
    path: { bot_id: botId },
    throwOnError: true,
  })
  return data as ACPSettings | undefined
}

export function createACPDefaults(deps: {
  currentBotId: Ref<string | null>
  sessionId: Ref<string | null>
  explicitSessionSelection: Ref<boolean>
  userScopeGeneration: () => number
  currentSelectRequest: () => number
  rememberDefault: (botId: string, input: ACPAgentSessionInput | null) => void
  cachedDefault: (botId: string) => {
    loaded: boolean
    input: ACPAgentSessionInput | null
  }
  pendingMatches: (input: ACPAgentSessionInput) => boolean
  stageDefault: (input: ACPAgentSessionInput) => void
}) {
  async function settingsForAgent(
    botId: string,
    agentId: string,
  ): Promise<Partial<ACPAgentSessionInput>> {
    try {
      const settings = await fetchACPSettings(botId)
      if (
        settings?.chat_runtime !== 'acp_agent'
        || (settings.chat_acp_agent_id ?? '').trim() !== agentId
      ) return {}
      return {
        projectPath: settings.chat_acp_project_path?.trim() || undefined,
        projectMode: settings.chat_acp_project_mode?.trim() || undefined,
      }
    } catch {
      return {}
    }
  }

  async function inputFromSettings(botId: string): Promise<ACPAgentSessionInput | null> {
    const bid = botId.trim()
    if (!bid) return null
    const generation = deps.userScopeGeneration()
    try {
      const settings = await fetchACPSettings(bid)
      if (generation !== deps.userScopeGeneration()) return null
      if (settings?.chat_runtime !== 'acp_agent') {
        deps.rememberDefault(bid, null)
        return null
      }
      const agentId = settings.chat_acp_agent_id?.trim() ?? ''
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

  async function defaultRuntimeIsACP(botId: string) {
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
    defaultRuntimeIsACP,
    stageFromSettings,
  }
}
