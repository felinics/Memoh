import type { Ref } from 'vue'
import type {
  ChatAttachment,
  CommandEventResponse,
} from '@/composables/api/useChat'
import { executeQuickAction } from '@/composables/api/useChat'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { BOT_AGENT_RUNTIME_CLAUDE_CODE, BOT_AGENT_RUNTIME_CODEX } from '@/utils/bot-agent'
import { createInvocationId } from '../chat-list.normalize'
import type { ExternalAgentSessionInput, ActiveChatTarget, ChatViewTarget } from './types'
import type { WebCommandResult } from './send'

interface DraftCommand {
  isCurrent: () => boolean
  finish: () => void
}

export interface ChatCommandDeps {
  currentBotId: Ref<string | null>
  userScopeGeneration: () => number
  isFocusedTarget: (target: ChatViewTarget) => boolean
  beginDraftCommand: (target: ChatViewTarget) => DraftCommand
  requestDraftView: (
    target: ChatViewTarget,
    input: ExternalAgentSessionInput | null,
    activate: boolean,
  ) => void
  ensureBot: () => Promise<string | null>
  defaultExternalAgentSettingsForAgent: (
    botId: string,
    agentId: string,
  ) => Promise<Partial<ExternalAgentSessionInput>>
  normalizeTarget: (target?: Partial<ChatViewTarget>) => ChatViewTarget
  chatTargetFor: (target: ChatViewTarget) => ActiveChatTarget
  commandErrorMessage: (code: string) => string
  showCommandError: (
    code: string,
    message: string,
    scope: { botId: string; sessionId?: string; composerScope?: string },
  ) => void
  rememberCommandEvent: (
    event: CommandEventResponse,
    scope: { botId: string; sessionId?: string; composerScope?: string },
  ) => void
  refreshACPRuntime: (botId: string, sessionId: string) => Promise<unknown>
}

function parseWebNewCommand(
  text: string,
): { mode: 'chat' | 'discuss' | ''; agentId: string } | null {
  const input = text.trim()
  if (!input.startsWith('/new')) return null
  const parts = input.split(/\s+/)
  if (parts[0] !== '/new') return null
  const positional = parts.slice(1).filter(part => part && !part.startsWith('-'))
  const first = positional[0]?.toLowerCase() ?? ''
  const second = positional[1]?.toLowerCase() ?? ''
  if (first === 'chat' || first === 'discuss') {
    return { mode: first, agentId: second }
  }
  return { mode: '', agentId: first }
}

export function createChatCommands(deps: ChatCommandDeps) {
  function isWebSlashInput(text: string): boolean {
    return text.trim().startsWith('/')
  }

  function quickActionIDForSlash(text: string): string {
    const parts = text.trim().split(/\s+/)
    const command = parts[0]?.toLowerCase() ?? ''
    const action = parts[1]?.toLowerCase() ?? ''
    if (command === '/help' && !action) return 'help'
    if (command === '/skill' && (!action || action === 'list')) return 'skill.list'
    if (command === '/permission') return 'permission'
    return ''
  }

  async function handleWebNewCommand(
    text: string,
    attachments: ChatAttachment[] | undefined,
    target: ChatViewTarget,
  ): Promise<WebCommandResult> {
    const parsed = parseWebNewCommand(text)
    if (!parsed) return { kind: 'none' }
    const generation = deps.userScopeGeneration()
    const activate = deps.isFocusedTarget(target)
    if (attachments?.length) {
      return { kind: 'error', message: 'Attachments are not supported with /new' }
    }
    const agentId = parsed.agentId.trim()
    if (!agentId) {
      if (parsed.mode === 'discuss') {
        return {
          kind: 'error',
          message: 'Discuss External Agent sessions require an agent, for example /new discuss codex',
        }
      }
      const command = deps.beginDraftCommand(target)
      if (command.isCurrent()) deps.requestDraftView(target, null, activate)
      command.finish()
      return { kind: 'handled' }
    }
    if (agentId !== BOT_AGENT_RUNTIME_CODEX && agentId !== BOT_AGENT_RUNTIME_CLAUDE_CODE) {
      return { kind: 'error', message: `Unknown agent "${agentId}" — use /new codex or /new claude-code, or pick an agent from the composer` }
    }

    const command = deps.beginDraftCommand(target)
    try {
      const targetBotId = target.botId === '__unbound__' ? '' : target.botId
      // ensureBot now rethrows fetch failures (so bootstrap recovery can retry
      // them); this interactive path keeps its original "not ready" reply.
      const botId = targetBotId || await deps.ensureBot().catch(() => null)
      if (!botId) return { kind: 'error', message: 'Bot not ready' }
      const defaults = await deps.defaultExternalAgentSettingsForAgent(botId, agentId)
      if (
        generation !== deps.userScopeGeneration()
        || (deps.currentBotId.value ?? '').trim() !== botId
        || !command.isCurrent()
      ) {
        return { kind: 'handled' }
      }
      deps.requestDraftView(target, {
        agentId,
        sessionMode: parsed.mode === 'discuss' ? 'discuss' : 'chat',
        ...defaults,
        // codex / claude-code are direct runtimes, not ACP profiles; without
        // the explicit runtime the draft would create an acp_agent session
        // the server refuses.
        runtime: agentId === BOT_AGENT_RUNTIME_CODEX ? BOT_AGENT_RUNTIME_CODEX : BOT_AGENT_RUNTIME_CLAUDE_CODE,
      }, activate)
      return { kind: 'handled' }
    } finally {
      command.finish()
    }
  }

  async function handleWebSlashCommand(
    text: string,
    hasRequestedSkills = false,
    composerScope = 'chat',
    target?: ChatViewTarget,
  ): Promise<WebCommandResult> {
    if (!isWebSlashInput(text) || hasRequestedSkills) return { kind: 'none' }
    const resolved = deps.normalizeTarget(target)
    const botId = resolved.botId
    if (!botId) return { kind: 'error', message: 'Bot not selected' }
    const sessionId = resolved.sessionId ?? ''
    const scope = composerScope.trim() || 'chat'
    const commandScope = {
      botId,
      sessionId: sessionId || undefined,
      composerScope: scope,
    }

    const actionId = quickActionIDForSlash(text)
    if (!actionId) return { kind: 'none' }
    const skillActivationAllowed = !deps.chatTargetFor(resolved).isExternalAgent
    let event: CommandEventResponse | null
    try {
      event = await executeQuickAction(botId, actionId, {
        invocationId: createInvocationId(),
        composerScope: scope,
        sessionId: sessionId || undefined,
        skillActivationAllowed,
        modeId: actionId === 'permission'
          ? text.trim().replace(/^\/permission(?:\s+|$)/i, '').trim() || undefined
          : undefined,
      })
    } catch (error) {
      const message = resolveApiErrorMessage(error, deps.commandErrorMessage('generic'))
      deps.showCommandError('generic', message, commandScope)
      return { kind: 'error', message }
    }

    if (!event) return { kind: 'none' }
    deps.rememberCommandEvent(event, commandScope)
    if (event.type === 'command_error') {
      return {
        kind: 'error',
        message: event.error?.message || deps.commandErrorMessage('generic'),
      }
    }
    if (actionId === 'permission' && sessionId) {
      // The command endpoint returns a presentation envelope, while the
      // registry owns the full runtime status used by the composer controls.
      // Refresh after both list and set so the mode selector cannot remain on
      // the pre-command snapshot.
      await deps.refreshACPRuntime(botId, sessionId).catch(() => undefined)
    }
    return { kind: 'handled' }
  }

  return {
    handleWebNewCommand,
    isWebSlashInput,
    quickActionIDForSlash,
    handleWebSlashCommand,
  }
}
