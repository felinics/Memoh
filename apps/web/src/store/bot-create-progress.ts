import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { getAgentAuthorizationsById, getBotsByBotIdAgents, getBotsByBotIdAgentsById, patchBotsByBotIdAgentsById, postBotsByBotIdAgents, postBotsByBotIdAgentsByIdCredentialClaim, postBotsByBotIdUserAccess, putBotsByBotIdSettings } from '@memohai/sdk'
import type { BotagentsBotAgent, BotsBot, BotsCreateBotRequest } from '@memohai/sdk'
import {
  botCreateProgressPercent,
  collectBotCreateProgressStream,
  postBotsStream,
  type BotCreateProgress,
} from '@/composables/api/useBotCreateStream'
import {
  appendBotCreateTerminalLine,
  finalizeBotCreateTerminalLines,
  pushBotCreateTerminalLine,
  type BotCreateTerminalLine,
} from '@/composables/api/botCreateTerminal'
import { apiErrorStatus, parseMemohError, resolveApiErrorMessage } from '@/utils/api-error'
import { botAgentRuntimeForProvider, directBotAgentMetadata } from '@/utils/bot-agent'
import { externalAgentDisplayName } from '@/utils/external-agent'
import { writeCreatedAgentSession, type CreatedAgentSession } from '@/pages/bots/created-agent-session'
import { installCreatedAgent } from './install-created-agent'

// A setup failure keeps the created Bot and retries only its remaining setup.
export type BotCreateStatus = 'idle' | 'creating' | 'ready' | 'setup-error' | 'error'

export type BotCreateDisplay = {
  display_name: string
  name?: string
  avatar_url?: string
}

export type BotCreateSettings = {
  chat_model_id?: string
  memory_provider_id?: string
  reasoning_effort?: string
}

export type BotCreateAgent = {
  authorizationId?: string
  name: string
  provider: string
  metadata?: Record<string, unknown>
}

// Workspace access drafted on the create form. The creator's own grant is not
// here — the server writes that itself when the bot is created — so this only
// ever carries the members added alongside them.
export type BotCreateGrant = {
  subject_type: 'user' | 'everyone'
  user_id?: string
  permissions: string[]
}

export type StartBotCreateOptions = {
  onboarding?: boolean
  display?: BotCreateDisplay
  settings?: BotCreateSettings
  agent?: BotCreateAgent
  grants?: BotCreateGrant[]
}

export type BotCreateStartResult = {
  settingsApplied: boolean
  agentApplied: boolean
  agentId?: string
}

function hasSettings(settings?: BotCreateSettings): boolean {
  return !!(settings && (settings.chat_model_id || settings.memory_provider_id || settings.reasoning_effort))
}

function settingsBody(settings: BotCreateSettings) {
  return {
    ...(settings.chat_model_id ? { chat_model_id: settings.chat_model_id } : {}),
    ...(settings.memory_provider_id ? { memory_provider_id: settings.memory_provider_id } : {}),
    ...(settings.reasoning_effort ? { reasoning_effort: settings.reasoning_effort } : {}),
  }
}

// Grants are applied one at a time and never fail the creation: the bot and its
// owner already exist, so a rejected member is a partial share to fix on the
// Access Control tab, not a reason to present the whole create as broken. Each
// failure still surfaces as the setup error the progress view reads.
async function applyGrants(
  botId: string,
  grants: BotCreateGrant[] | undefined,
  onError?: (message: string) => void,
): Promise<void> {
  for (const grant of grants ?? []) {
    if (grant.subject_type === 'user' && !grant.user_id) continue
    if (grant.permissions.length === 0) continue
    try {
      await postBotsByBotIdUserAccess({
        path: { bot_id: botId },
        body: {
          subject_type: grant.subject_type,
          user_id: grant.subject_type === 'user' ? grant.user_id : undefined,
          permissions: grant.permissions,
        },
        throwOnError: true,
      })
    } catch (error) {
      onError?.(resolveApiErrorMessage(error, toMessage(error)))
    }
  }
}

function toMessage(error: unknown): string {
  if (error instanceof Error) return error.message
  if (typeof error === 'string' && error.trim()) return error
  return 'Bot create failed'
}

// Owns the bot-create SSE stream and derived state so it survives navigation
// from the create form to the dedicated progress route. Views read this store
// and own navigation/onboarding side effects.
export const useBotCreateProgressStore = defineStore('bot-create-progress', () => {
  const status = ref<BotCreateStatus>('idle')
  const display = ref<BotCreateDisplay | null>(null)
  const progress = ref<BotCreateProgress | null>(null)
  const lines = ref<BotCreateTerminalLine[]>([])
  const bot = ref<BotsBot | null>(null)
  const createdAgent = ref<BotagentsBotAgent | null>(null)
  const authorizationId = ref('')
  const setupError = ref<string | null>(null)
  const errorCode = ref<string | null>(null)
  const modelConfigured = ref(false)

  let lastPayload: BotsCreateBotRequest | null = null
  let lastOptions: StartBotCreateOptions = {}

  const percent = computed(() => botCreateProgressPercent(progress.value))
  const isActive = computed(() => status.value === 'creating')

  function reset() {
    status.value = 'idle'
    display.value = null
    progress.value = null
    lines.value = []
    bot.value = null
    createdAgent.value = null
    authorizationId.value = ''
    setupError.value = null
    errorCode.value = null
    lastPayload = null
    writeCreatedAgentSession(null, lastOptions.onboarding)
    lastOptions = {}
    modelConfigured.value = false
  }

  function ensureErrorLine(message: string) {
    if (lines.value.at(-1)?.kind === 'error') return
    lines.value = appendBotCreateTerminalLine(lines.value, { type: 'error', message })
  }

  function saveSession() {
    const runtime = lastOptions.agent && botAgentRuntimeForProvider(lastOptions.agent.provider)
    if (!bot.value?.id || (runtime !== 'codex' && runtime !== 'claude-code')) return
    writeCreatedAgentSession({
      botId: bot.value.id, botName: bot.value.name ?? '', displayName: display.value?.display_name ?? '',
      agentId: createdAgent.value?.id ?? '', runtime, authorizationId: authorizationId.value,
      settings: lastOptions.settings, setupError: setupError.value,
    }, lastOptions.onboarding)
  }

  async function applySetup(recovering = false): Promise<BotCreateStartResult> {
    const options = lastOptions
    const botId = bot.value?.id
    let settingsApplied = !hasSettings(options.settings)
    let agentApplied = !options.agent
    const directAgent = !!options.agent && botAgentRuntimeForProvider(options.agent.provider) !== 'acp'
    if (!botId) return { settingsApplied: false, agentApplied: false }
    try {
      if (hasSettings(options.settings) || options.agent) {
        lines.value = pushBotCreateTerminalLine(lines.value, { kind: 'applying-settings', status: 'running' })
      }
      if (options.agent) {
        const provider = options.agent.provider.trim().toLowerCase()
        const runtime = botAgentRuntimeForProvider(provider)
        let metadata = options.agent.metadata ?? { provider }
        // Reconcile lost responses and refreshes before creating or claiming again.
        if (recovering) {
          if (createdAgent.value?.id) {
            const { data } = await getBotsByBotIdAgentsById({ path: { bot_id: botId, id: createdAgent.value.id }, throwOnError: true })
            createdAgent.value = data
          } else {
            const { data } = await getBotsByBotIdAgents({ path: { bot_id: botId }, throwOnError: true })
            createdAgent.value = data.items?.find(agent => agent.runtime === runtime) ?? null
          }
          if (authorizationId.value && !createdAgent.value?.agent_credential_id) {
            const { data } = await getAgentAuthorizationsById({ path: { id: authorizationId.value }, throwOnError: true })
            metadata = { ...metadata, auth: data.auth_kind === 'openai_codex_oauth' ? 'chatgpt' : data.auth_kind === 'claude_code_oauth' ? 'oauth_token' : 'api_key' }
          }
        }
        if (!createdAgent.value?.id) {
          const { data } = await postBotsByBotIdAgents({
            path: { bot_id: botId },
            body: { name: options.agent.name.trim(), runtime, ...(directAgent && { enabled: false }), metadata },
            throwOnError: true,
          })
          createdAgent.value = { ...data, runtime: data.runtime ?? runtime }
          saveSession()
        }
        const agentId = createdAgent.value.id?.trim()
        if (!agentId) throw new Error('Created Agent has no ID')
        if (authorizationId.value && !createdAgent.value.agent_credential_id) {
          if (recovering && createdAgent.value.metadata?.auth !== metadata.auth) {
            await patchBotsByBotIdAgentsById({ path: { bot_id: botId, id: agentId }, body: { metadata }, throwOnError: true })
          }
          const { data } = await postBotsByBotIdAgentsByIdCredentialClaim({
            path: { bot_id: botId, id: agentId }, body: { authorization_id: authorizationId.value }, throwOnError: true,
          })
          createdAgent.value.agent_credential_id = data.id
        }
      }
      if (hasSettings(options.settings) || (createdAgent.value?.id && !directAgent)) {
        await putBotsByBotIdSettings({
          path: { bot_id: botId },
          body: { ...settingsBody(options.settings ?? {}), ...(!directAgent && createdAgent.value?.id ? { default_bot_agent_id: createdAgent.value.id } : {}) },
          throwOnError: true,
        })
        settingsApplied = true
        if (!directAgent) agentApplied = true
      }
      modelConfigured.value = !!options.settings?.chat_model_id && settingsApplied
      lines.value = finalizeBotCreateTerminalLines(lines.value)
      if (directAgent && createdAgent.value?.id) {
        const agent = createdAgent.value
        const agentId = agent.id!
        lines.value = pushBotCreateTerminalLine(lines.value, {
          kind: 'installing-agent', status: 'running', message: externalAgentDisplayName(agent.runtime ?? '', agent.name ?? ''),
        })
        await installCreatedAgent(botId, agent)
        if (!agent.enabled) {
          await patchBotsByBotIdAgentsById({ path: { bot_id: botId, id: agentId }, body: { enabled: true }, throwOnError: true })
          agent.enabled = true
        }
        await putBotsByBotIdSettings({ path: { bot_id: botId }, body: { default_bot_agent_id: agentId }, throwOnError: true })
        agentApplied = true
        lines.value = finalizeBotCreateTerminalLines(lines.value)
      }
      if (!setupError.value) lines.value = pushBotCreateTerminalLine(lines.value, { kind: 'ready', status: 'done' })
      status.value = 'ready'
    } catch (error) {
      setupError.value = resolveApiErrorMessage(error, toMessage(error))
      errorCode.value = parseMemohError(error)?.code ?? null
      lines.value = finalizeBotCreateTerminalLines(lines.value, 'error')
      ensureErrorLine(setupError.value)
      status.value = directAgent ? 'setup-error' : 'ready'
    }
    saveSession()
    return { settingsApplied, agentApplied, agentId: createdAgent.value?.id }
  }

  async function start(
    payload: BotsCreateBotRequest,
    options: StartBotCreateOptions = {},
  ): Promise<BotCreateStartResult> {
    if (status.value === 'creating') return { settingsApplied: false, agentApplied: false }
    lastPayload = payload
    lastOptions = options
    writeCreatedAgentSession(null, options.onboarding)
    status.value = 'creating'
    bot.value = null
    createdAgent.value = null
    authorizationId.value = options.agent?.authorizationId ?? ''
    setupError.value = null
    errorCode.value = null
    modelConfigured.value = false
    progress.value = { phase: 'pulling' }
    display.value = options.display ?? { display_name: payload.display_name ?? payload.name ?? '', avatar_url: payload.avatar_url }
    lines.value = pushBotCreateTerminalLine([], { kind: 'command', status: 'info', message: display.value.display_name })
    try {
      const { stream } = await postBotsStream({ body: payload, throwOnError: true })
      const result = await collectBotCreateProgressStream(stream, {
        onState: (state) => {
          progress.value = state.progress ?? progress.value
          if (state.bot) { bot.value = state.bot; saveSession() }
        },
        onEvent: (event) => {
          // Installation and Agent activation must finish before the ready line.
          if (event.type !== 'ready') lines.value = appendBotCreateTerminalLine(lines.value, event)
        },
      })
      bot.value = result.bot ?? null
      setupError.value = result.setupError ?? null
      errorCode.value = result.errorCode ?? null
      if (!bot.value) {
        ensureErrorLine(result.setupError ?? toMessage(undefined))
        status.value = 'error'
        return { settingsApplied: false, agentApplied: false }
      }
      if (result.setupError && options.agent) {
        status.value = 'setup-error'
        saveSession()
        return { settingsApplied: false, agentApplied: false }
      }
      if (bot.value.id) await applyGrants(bot.value.id, options.grants, message => { setupError.value = message })
      return await applySetup()
    } catch (error) {
      const message = resolveApiErrorMessage(error, toMessage(error))
      setupError.value = message
      errorCode.value = parseMemohError(error)?.code ?? (apiErrorStatus(error) === 409 ? 'bot.name_taken' : null)
      progress.value = { phase: 'error', error: message }
      ensureErrorLine(message)
      status.value = bot.value ? options.agent ? 'setup-error' : 'ready' : 'error'
      saveSession()
      return { settingsApplied: false, agentApplied: false }
    }
  }

  async function retry() {
    if (status.value === 'creating') return
    if (bot.value?.id) {
      status.value = 'creating'
      setupError.value = null
      errorCode.value = null
      return await applySetup(true)
    }
    if (lastPayload) return await start(lastPayload, lastOptions)
  }

  function restore(saved: CreatedAgentSession, onboarding = false) {
    if (status.value !== 'idle') return
    bot.value = { id: saved.botId, name: saved.botName }
    display.value = { display_name: saved.displayName }
    createdAgent.value = saved.agentId ? { id: saved.agentId, runtime: saved.runtime } : null
    authorizationId.value = saved.authorizationId ?? ''
    lastOptions = { onboarding, settings: saved.settings, agent: {
      name: externalAgentDisplayName(saved.runtime, saved.runtime), provider: saved.runtime,
      metadata: directBotAgentMetadata(saved.runtime), authorizationId: saved.authorizationId,
    } }
    lines.value = pushBotCreateTerminalLine([], { kind: 'bot-created', status: 'done' })
    return retry()
  }

  return {
    status,
    display,
    progress,
    lines,
    bot,
    createdAgent,
    authorizationId,
    setupError,
    errorCode,
    modelConfigured,
    restore,
    percent,
    isActive,
    start,
    retry,
    reset,
  }
})
