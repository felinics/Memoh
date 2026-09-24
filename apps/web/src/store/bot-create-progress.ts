import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { getAgentAuthorizationsById, getBotsById, getBotsByIdChecks, getBotsByBotIdAgents, getBotsByBotIdAgentsById, patchBotsByBotIdAgentsById, postBotsByBotIdAgents, postBotsByBotIdAgentsByIdCredentialClaim, postBotsByBotIdUserAccess, putBotsByBotIdSettings } from '@memohai/sdk'
import type { BotagentsBotAgent, BotsBot, BotsCreateBotRequest } from '@memohai/sdk'
import {
  botCreateProgressPercent,
  collectBotCreateProgressStream,
  postBotsStream,
  type BotCreateProgress,
  type BotCreateStreamEvent,
} from '@/composables/api/useBotCreateStream'
import { postBotsByBotIdContainerStream } from '@/composables/api/useContainerStream'
import {
  appendBotCreateTerminalLine,
  finalizeBotCreateTerminalLines,
  pushBotCreateTerminalLine,
  type BotCreateTerminalLine,
} from '@/composables/api/botCreateTerminal'
import { apiErrorStatus, parseMemohError, resolveApiErrorMessage } from '@/utils/api-error'
import { botAgentRuntimeForProvider, directBotAgentMetadata } from '@/utils/bot-agent'
import { externalAgentDisplayName } from '@/utils/external-agent'
import { writeCreatedBotSession, type CreatedBotSession, type CreatedBotSessionRuntime } from '@/pages/bots/created-bot-session'
import { randomUUID } from '@/utils/uuid'
import { installCreatedAgent } from './install-created-agent'

// The Bot row exists from the first `bot_created` event on, so every failure
// after it keeps the Bot and retries only what is missing:
// - workspace-error: the workspace never became ready (bot status `failed`);
//   retry re-asserts the workspace intent on the same Bot.
// - setup-error: the workspace is fine but the Agent / settings step failed;
//   retry re-runs only that step.
// - error: no Bot exists (rejected create, or the Bot was deleted meanwhile).
export type BotCreateStatus = 'idle' | 'creating' | 'ready' | 'setup-error' | 'workspace-error' | 'error'

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

const NOTHING_APPLIED: BotCreateStartResult = { settingsApplied: false, agentApplied: false }

// How a resumed flow follows a Bot that is still `creating`. The budget mirrors
// the server's SSE stream budget; past it the workspace page is the place to
// keep watching.
export const BOT_STATUS_POLL_INTERVAL_MS = 2000
export const BOT_STATUS_POLL_BUDGET_MS = 15 * 60 * 1000

const WORKSPACE_FAILURE_FALLBACK = { i18n_key: 'bots.create.failedSubtitle' }
const WORKSPACE_STILL_PROVISIONING = { i18n_key: 'bots.create.stillProvisioning' }
const BOT_STATUS_FAILED = 'failed'
const BOT_STATUS_CREATING = 'creating'
const CONTAINER_INIT_CHECK = 'container.init'

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

function directRuntimeOf(agent?: BotCreateAgent): CreatedBotSessionRuntime | undefined {
  if (!agent) return undefined
  const runtime = botAgentRuntimeForProvider(agent.provider)
  return runtime === 'codex' || runtime === 'claude-code' ? runtime : undefined
}

function sleep(ms: number): Promise<void> {
  return new Promise(resolve => setTimeout(resolve, ms))
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

// Polls the Bot until the server has settled its workspace one way or the
// other. Returns the last observation even when the budget runs out while the
// Bot is still creating; the caller decides what that means.
async function awaitBotSettled(botId: string): Promise<BotsBot> {
  const deadline = Date.now() + BOT_STATUS_POLL_BUDGET_MS
  for (;;) {
    const { data } = await getBotsById({ path: { id: botId }, throwOnError: true })
    if (data.status !== BOT_STATUS_CREATING || Date.now() >= deadline) return data
    await sleep(BOT_STATUS_POLL_INTERVAL_MS)
  }
}

// The failure reason of a `failed` Bot lives in its runtime checks; the
// initialization check carries the reconciler's last error.
async function workspaceFailureDetail(botId: string): Promise<string> {
  try {
    const { data } = await getBotsByIdChecks({ path: { id: botId }, throwOnError: true })
    const detail = data.items?.find(check => check.type === CONTAINER_INIT_CHECK && check.status === 'error')?.detail?.trim()
    if (detail) return detail
  } catch {
    // The check list is a nicety; the failure itself is already known.
  }
  return resolveApiErrorMessage(WORKSPACE_FAILURE_FALLBACK, 'Workspace setup failed')
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
  const hasPayload = ref(false)

  let lastPayload: BotsCreateBotRequest | null = null
  let lastOptions: StartBotCreateOptions = {}
  // Idempotency-Key of the last create. retry() resends it with the same
  // payload, so a create whose response was lost is answered with the Bot it
  // already made instead of a second one; every start() from the form mints a
  // new key.
  let lastRequestKey = ''
  let grantsApplied = false

  const percent = computed(() => botCreateProgressPercent(progress.value))
  const isActive = computed(() => status.value === 'creating')
  // A retry needs something to retry on: an existing Bot, or the rejected payload.
  const canRetry = computed(() => status.value !== 'creating' && (!!bot.value?.id || hasPayload.value))

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
    hasPayload.value = false
    grantsApplied = false
    writeCreatedBotSession(null, lastOptions.onboarding)
    lastOptions = {}
    modelConfigured.value = false
  }

  function ensureErrorLine(message: string) {
    if (lines.value.at(-1)?.kind === 'error') return
    lines.value = appendBotCreateTerminalLine(lines.value, { type: 'error', message })
  }

  function saveSession() {
    if (!bot.value?.id) return
    const runtime = directRuntimeOf(lastOptions.agent)
    writeCreatedBotSession({
      botId: bot.value.id, botName: bot.value.name ?? '', displayName: display.value?.display_name ?? '',
      avatarUrl: display.value?.avatar_url, settings: lastOptions.settings, setupError: setupError.value,
      ...(runtime && { runtime, agentId: createdAgent.value?.id ?? '', authorizationId: authorizationId.value || undefined }),
    }, lastOptions.onboarding)
  }

  function beginStep() {
    status.value = 'creating'
    setupError.value = null
    errorCode.value = null
  }

  function failWorkspace(message: string, code: string | null) {
    setupError.value = message
    errorCode.value = code
    progress.value = { phase: 'error', error: message }
    lines.value = finalizeBotCreateTerminalLines(lines.value, 'error')
    ensureErrorLine(message)
    status.value = 'workspace-error'
    saveSession()
  }

  function failWithoutBot(error: unknown) {
    const message = resolveApiErrorMessage(error, toMessage(error))
    setupError.value = message
    errorCode.value = parseMemohError(error)?.code ?? (apiErrorStatus(error) === 409 ? 'bot.name_taken' : null)
    progress.value = { phase: 'error', error: message }
    ensureErrorLine(message)
    status.value = 'error'
  }

  async function applySetup(recovering = false): Promise<BotCreateStartResult> {
    const options = lastOptions
    const botId = bot.value?.id
    let settingsApplied = !hasSettings(options.settings)
    let agentApplied = !options.agent
    const directAgent = !!options.agent && botAgentRuntimeForProvider(options.agent.provider) !== 'acp'
    if (!botId) return NOTHING_APPLIED
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

  // Everything that happens once the workspace is ready. Grants are applied
  // once per Bot; a workspace retry or a resumed flow does not re-share.
  async function finishSetup(recovering = false): Promise<BotCreateStartResult> {
    const botId = bot.value?.id
    if (botId && !grantsApplied) {
      grantsApplied = true
      await applyGrants(botId, lastOptions.grants, message => { setupError.value = message })
    }
    return await applySetup(recovering)
  }

  // Relays a workspace provisioning stream (create or container retry) into
  // the terminal. Returns the settled state; a stream that broke without a
  // server error event leaves `errorCode` empty.
  async function followWorkspaceStream(stream: AsyncGenerator<BotCreateStreamEvent, void, unknown>) {
    return await collectBotCreateProgressStream(stream, {
      initialState: bot.value ? { bot: bot.value } : undefined,
      onState: (state) => {
        progress.value = state.progress ?? progress.value
        if (state.bot) { bot.value = state.bot; saveSession() }
      },
      onEvent: (event) => {
        // Installation and Agent activation must finish before the ready line.
        if (event.type !== 'ready') lines.value = appendBotCreateTerminalLine(lines.value, event)
      },
    })
  }

  // Follows an existing Bot through the server's own status instead of posting
  // a second create: after a refresh, a lost stream, or a workspace retry whose
  // stream ended early. The reconciler keeps working while nobody is watching.
  async function resume(): Promise<BotCreateStartResult> {
    const botId = bot.value?.id
    if (!botId) return NOTHING_APPLIED
    beginStep()
    if (lines.value.at(-1)?.kind !== 'creating') {
      lines.value = pushBotCreateTerminalLine(lines.value, { kind: 'creating', status: 'running' })
    }
    progress.value = { phase: 'creating' }
    try {
      const current = await awaitBotSettled(botId)
      bot.value = { ...bot.value, ...current }
      if (!display.value?.display_name && current.display_name) {
        display.value = { display_name: current.display_name, avatar_url: current.avatar_url }
      }
      if (current.status === BOT_STATUS_FAILED) {
        failWorkspace(await workspaceFailureDetail(botId), 'workspace_setup_failed')
        return NOTHING_APPLIED
      }
      if (current.status === BOT_STATUS_CREATING) {
        failWorkspace(resolveApiErrorMessage(WORKSPACE_STILL_PROVISIONING, 'Workspace setup is still in progress'), 'workspace_setup_timeout')
        return NOTHING_APPLIED
      }
      lines.value = finalizeBotCreateTerminalLines(lines.value)
      return await finishSetup(true)
    } catch (error) {
      if (apiErrorStatus(error) === 404) {
        // The Bot was deleted meanwhile; there is nothing left to resume on.
        bot.value = null
        writeCreatedBotSession(null, lastOptions.onboarding)
        failWithoutBot(error)
        return NOTHING_APPLIED
      }
      failWorkspace(resolveApiErrorMessage(error, toMessage(error)), parseMemohError(error)?.code ?? null)
      return NOTHING_APPLIED
    }
  }

  // Re-asserts the workspace intent on the same Bot. The server bumps the
  // desired generation and provisions again; no new Bot, no new name.
  async function retryWorkspace(): Promise<BotCreateStartResult> {
    const botId = bot.value?.id
    if (!botId) return NOTHING_APPLIED
    beginStep()
    progress.value = { phase: 'pulling' }
    try {
      const { stream } = await postBotsByBotIdContainerStream({ path: { bot_id: botId }, body: {}, throwOnError: true })
      const result = await followWorkspaceStream(stream)
      if (result.setupError) {
        if (!result.errorCode) return await resume()
        failWorkspace(result.setupError, result.errorCode)
        return NOTHING_APPLIED
      }
      lines.value = finalizeBotCreateTerminalLines(lines.value)
      return await finishSetup()
    } catch (error) {
      failWorkspace(resolveApiErrorMessage(error, toMessage(error)), parseMemohError(error)?.code ?? null)
      return NOTHING_APPLIED
    }
  }

  async function start(
    payload: BotsCreateBotRequest,
    options: StartBotCreateOptions = {},
    requestKey: string = randomUUID(),
  ): Promise<BotCreateStartResult> {
    if (status.value === 'creating') return NOTHING_APPLIED
    lastPayload = payload
    hasPayload.value = true
    lastOptions = options
    lastRequestKey = requestKey
    grantsApplied = false
    writeCreatedBotSession(null, options.onboarding)
    beginStep()
    bot.value = null
    createdAgent.value = null
    authorizationId.value = options.agent?.authorizationId ?? ''
    modelConfigured.value = false
    progress.value = { phase: 'pulling' }
    display.value = options.display ?? { display_name: payload.display_name ?? payload.name ?? '', avatar_url: payload.avatar_url }
    lines.value = pushBotCreateTerminalLine([], { kind: 'command', status: 'info', message: display.value.display_name })
    try {
      const { stream } = await postBotsStream({ body: payload, headers: { 'Idempotency-Key': requestKey }, throwOnError: true })
      const result = await followWorkspaceStream(stream)
      bot.value = result.bot ?? null
      if (!bot.value) {
        setupError.value = result.setupError ?? null
        errorCode.value = result.errorCode ?? null
        ensureErrorLine(result.setupError ?? toMessage(undefined))
        status.value = 'error'
        return NOTHING_APPLIED
      }
      if (result.setupError) {
        // A stream that broke without a server error event says nothing about
        // the workspace itself; follow the Bot instead of declaring it failed.
        if (!result.errorCode) return await resume()
        failWorkspace(result.setupError, result.errorCode)
        return NOTHING_APPLIED
      }
      return await finishSetup()
    } catch (error) {
      if (bot.value) {
        failWorkspace(resolveApiErrorMessage(error, toMessage(error)), parseMemohError(error)?.code ?? null)
        return NOTHING_APPLIED
      }
      failWithoutBot(error)
      return NOTHING_APPLIED
    }
  }

  async function retry() {
    if (status.value === 'creating') return
    if (status.value === 'workspace-error' && bot.value?.id) return await retryWorkspace()
    if (bot.value?.id) {
      beginStep()
      return await applySetup(true)
    }
    if (lastPayload) return await start(lastPayload, lastOptions, lastRequestKey)
  }

  function restore(saved: CreatedBotSession, onboarding = false) {
    if (status.value !== 'idle') return
    bot.value = { id: saved.botId, name: saved.botName }
    display.value = { display_name: saved.displayName, avatar_url: saved.avatarUrl }
    createdAgent.value = saved.runtime && saved.agentId ? { id: saved.agentId, runtime: saved.runtime } : null
    authorizationId.value = saved.authorizationId ?? ''
    // Member grants are not part of the saved target; a resumed flow never re-shares.
    grantsApplied = true
    lastOptions = {
      onboarding,
      settings: saved.settings,
      ...(saved.runtime && { agent: {
        name: externalAgentDisplayName(saved.runtime, saved.runtime), provider: saved.runtime,
        metadata: directBotAgentMetadata(saved.runtime), authorizationId: saved.authorizationId,
      } }),
    }
    lines.value = pushBotCreateTerminalLine([], { kind: 'bot-created', status: 'done' })
    return resume()
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
    canRetry,
    start,
    retry,
    reset,
  }
})
