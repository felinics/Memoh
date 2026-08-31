import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { postBotsByBotIdAgents, putBotsByBotIdSettings } from '@memohai/sdk'
import type { BotsBot, BotsCreateBotRequest } from '@memohai/sdk'
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
import { botAgentRuntimeForProvider } from '@/utils/bot-agent'

// status reflects the bot-create lifecycle:
//   idle     - nothing in flight (also the guard for the progress route)
//   creating - the SSE stream is running
//   ready    - a bot exists (possibly with a non-fatal setupError warning)
//   error    - a hard failure where no bot was created
export type BotCreateStatus = 'idle' | 'creating' | 'ready' | 'error'

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
  name: string
  provider: string
  metadata?: Record<string, unknown>
}

export type StartBotCreateOptions = {
  display?: BotCreateDisplay
  settings?: BotCreateSettings
  agent?: BotCreateAgent
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
  const setupError = ref<string | null>(null)
  const errorCode = ref<string | null>(null)

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
    setupError.value = null
    errorCode.value = null
    lastPayload = null
    lastOptions = {}
  }

  function ensureErrorLine(message: string) {
    if (lines.value.at(-1)?.kind === 'error') return
    lines.value = appendBotCreateTerminalLine(lines.value, { type: 'error', message })
  }

  async function start(
    payload: BotsCreateBotRequest,
    options: StartBotCreateOptions = {},
  ): Promise<BotCreateStartResult> {
    if (status.value === 'creating') return { settingsApplied: false, agentApplied: false }
    let settingsApplied = !hasSettings(options.settings)
    let agentApplied = !options.agent
    let createdAgentID = ''
    lastPayload = payload
    lastOptions = options

    status.value = 'creating'
    bot.value = null
    setupError.value = null
    errorCode.value = null
    progress.value = { phase: 'pulling' }
    display.value = options.display ?? {
      display_name: payload.display_name ?? payload.name ?? '',
      avatar_url: payload.avatar_url,
    }
    lines.value = pushBotCreateTerminalLine([], {
      kind: 'command',
      status: 'info',
      message: display.value.display_name,
    })

    try {
      const { stream } = await postBotsStream({ body: payload, throwOnError: true })
      const result = await collectBotCreateProgressStream(stream, {
        onState: (state) => {
          progress.value = state.progress ?? progress.value
        },
        onEvent: (event) => {
          // The final "ready" line is emitted after settings are applied so the
          // log reads naturally: creating, applying settings, ready.
          if (event.type === 'ready') return
          lines.value = appendBotCreateTerminalLine(lines.value, event)
        },
      })

      const createdBot = result.bot ?? null
      bot.value = createdBot
      setupError.value = result.setupError ?? null
      errorCode.value = result.errorCode ?? null

      if (!createdBot) {
        ensureErrorLine(result.setupError ?? toMessage(undefined))
        status.value = 'error'
        return { settingsApplied: false, agentApplied: false }
      }

      const botId = createdBot.id
      if (botId && (hasSettings(options.settings) || options.agent)) {
        lines.value = pushBotCreateTerminalLine(lines.value, { kind: 'applying-settings', status: 'running' })
        if (options.agent) {
          try {
            const provider = options.agent.provider.trim().toLowerCase()
            const { data: createdAgent } = await postBotsByBotIdAgents({
              path: { bot_id: botId },
              body: {
                name: options.agent.name.trim(),
                // codex / claude-code are direct runtimes; everything else is
                // an ACP profile provider.
                runtime: botAgentRuntimeForProvider(provider),
                metadata: options.agent.metadata ?? { provider },
              },
              throwOnError: true,
            })
            createdAgentID = createdAgent.id?.trim() ?? ''
            if (!createdAgentID) throw new Error('Created Agent has no ID')
          } catch (error) {
            setupError.value = resolveApiErrorMessage(error, toMessage(error))
            lines.value = finalizeBotCreateTerminalLines(lines.value, 'error')
          }
        }
        try {
          if (hasSettings(options.settings) || createdAgentID) {
            await putBotsByBotIdSettings({
              path: { bot_id: botId },
              body: {
                ...settingsBody(options.settings ?? {}),
                ...(createdAgentID ? { default_bot_agent_id: createdAgentID } : {}),
              },
              throwOnError: true,
            })
            if (hasSettings(options.settings)) settingsApplied = true
            if (createdAgentID) agentApplied = true
          }
        } catch (error) {
          // The bot exists, but its defaults are wrong — the created Agent is
          // not the default, or settings were dropped. Surface the failure
          // instead of showing a clean success over a half-configured bot.
          setupError.value = resolveApiErrorMessage(error, toMessage(error))
          lines.value = finalizeBotCreateTerminalLines(lines.value, 'error')
        }
        if (settingsApplied && agentApplied) {
          lines.value = finalizeBotCreateTerminalLines(lines.value)
        }
      }

      if (!result.setupError && !setupError.value) {
        lines.value = pushBotCreateTerminalLine(lines.value, { kind: 'ready', status: 'done' })
      }
      status.value = 'ready'
      return { settingsApplied, agentApplied, agentId: createdAgentID || undefined }
    } catch (error) {
      const parsed = parseMemohError(error)
      const message = resolveApiErrorMessage(error, toMessage(error))
      setupError.value = message
      errorCode.value = parsed?.code
        ?? (apiErrorStatus(error) === 409 ? 'bot.name_taken' : null)
      // If a bot was already created, a later failure (settings, terminal or
      // cache bookkeeping) is non-fatal: the bot exists, so never downgrade it
      // to a hard error — otherwise a successful create is reported as failed.
      if (bot.value) {
        status.value = 'ready'
        return { settingsApplied, agentApplied, agentId: createdAgentID || undefined }
      }
      progress.value = { phase: 'error', error: message }
      ensureErrorLine(message)
      status.value = 'error'
      return { settingsApplied: false, agentApplied: false }
    }
  }

  async function retry() {
    if (!lastPayload) return
    await start(lastPayload, lastOptions)
  }

  return {
    status,
    display,
    progress,
    lines,
    bot,
    setupError,
    errorCode,
    percent,
    isActive,
    start,
    retry,
    reset,
  }
})
