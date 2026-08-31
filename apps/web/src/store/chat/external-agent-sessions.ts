import type { Ref } from 'vue'
import {
  createSession,
  deleteSession,
  updateSessionAgent,
  type SessionSummary,
} from '@/composables/api/useChat'
import { BOT_AGENT_RUNTIME_ACP, BOT_AGENT_RUNTIME_CLAUDE_CODE, BOT_AGENT_RUNTIME_CODEX, botAgentRuntimeForProvider } from '@/utils/bot-agent'
import { provisionalSessionTitle } from '../chat-list.utils'
import { externalAgentDraftMetadata } from './external-agent-staging'
import { StreamFailureError } from './send'
import type { ExternalAgentSessionInput, ChatViewTarget } from './types'

interface PendingExternalAgentState {
  input: ExternalAgentSessionInput
  runtimeId: string
}

export interface ExternalAgentSessionDeps {
  currentBotId: Ref<string | null>
  sessionId: Ref<string | null>
  draftIntent: Ref<boolean>
  explicitSessionSelection: Ref<boolean>
  focusedViewId: Ref<string>
  userScopeGeneration: () => number
  normalizeTarget: (target?: Partial<ChatViewTarget>) => ChatViewTarget
  targetDraftForExternalAgent: (target?: ChatViewTarget) => ChatViewTarget
  pendingExternalAgentStateFor: (target: ChatViewTarget) => PendingExternalAgentState | null
  isFocusedTarget: (target: ChatViewTarget) => boolean
  upsertSession: (session: SessionSummary) => void
  rememberSession: (session: SessionSummary) => void
  promoteDraftView: (target: ChatViewTarget, sessionId: string) => void
  clearRuntimeStatus: (botId: string, runtimeId: string) => void
  forgetDraftStage: (target: ChatViewTarget) => void
  discardDraftStage: (target: ChatViewTarget) => void
  rememberDraftStage: (
    target: ChatViewTarget,
    detached: { botId: string; input: ExternalAgentSessionInput; runtimeId: string },
  ) => void
  activateDraftStage: (target: ChatViewTarget) => void
  markSessionDeleted: (botId: string, sessionId: string) => void
  stopSessionRuntime: (botId: string, sessionId: string) => void
  removeSessionView: (botId: string, sessionId: string) => void
  removeSessionFromList: (sessionId: string) => void
  ensureBot: () => Promise<string | null>
  knownSessionSummary: (sessionId: string) => SessionSummary | null | undefined
  isDraftCreationActive: (target: ChatViewTarget) => boolean
  beginDraftCreation: (target: ChatViewTarget) => void
  endDraftCreation: (target: ChatViewTarget) => void
  // Resolves the bot's working workdir for a new session ('' = no binding).
  // External Agent sessions can only bind native-workspace workdirs, so the resolver is
  // told which runtime the session will use.
  draftWorkdirIdFor: (botId: string, opts: { externalAgent: boolean }) => string
}

function normalizedExternalAgentInput(input: ExternalAgentSessionInput): ExternalAgentSessionInput {
  const metadata = externalAgentDraftMetadata(input)
  return {
    ...input,
    agentId: String(metadata.acp_agent_id ?? ''),
    projectPath: String(metadata.project_path ?? ''),
    projectMode: String(metadata.acp_project_mode ?? ''),
  }
}

function agentSessionRuntimeType(input: ExternalAgentSessionInput): string {
  switch (input.runtime) {
    case BOT_AGENT_RUNTIME_CODEX:
    case BOT_AGENT_RUNTIME_CLAUDE_CODE:
      return input.runtime
    case BOT_AGENT_RUNTIME_ACP:
      return 'acp_agent'
    default:
      break
  }
  // Legacy callers (slash commands, cached defaults) carry only agentId;
  // direct external agents are addressed by their runtime name, so derive
  // it here instead of silently creating an acp_agent session the server
  // will refuse.
  const derived = botAgentRuntimeForProvider(input.agentId)
  return derived === BOT_AGENT_RUNTIME_ACP ? 'acp_agent' : derived
}

function externalAgentSessionMetadata(input: ExternalAgentSessionInput): Record<string, unknown> {
  return agentSessionRuntimeType(input) === 'acp_agent' ? externalAgentDraftMetadata(input) : {}
}

export function createExternalAgentSessions(deps: ExternalAgentSessionDeps) {
  async function createExternalAgentSessionRecord(
    botId: string,
    input: ExternalAgentSessionInput,
  ): Promise<SessionSummary> {
    const id = botId.trim()
    if (!id) throw new Error('Bot not ready')
    const runtimeType = agentSessionRuntimeType(input)
    const runtimeMetadata = externalAgentSessionMetadata(input)
    const runtimeId = input.runtimeId?.trim() ?? ''
    const sessionMode = input.sessionMode === 'discuss' ? 'discuss' : 'chat'
    const workdirId = deps.draftWorkdirIdFor(id, { externalAgent: true })
    return createSession(id, {
      botAgentId: input.botAgentId,
      title: input.title ?? '',
      type: sessionMode,
      sessionMode,
      runtimeType,
      metadata: {},
      runtimeMetadata,
      acpRuntimeId: runtimeType === 'acp_agent' ? runtimeId || undefined : undefined,
      workdirId: workdirId || undefined,
    })
  }

  async function rollbackFailedCreation(
    created: SessionSummary,
    draft: ChatViewTarget,
    stagedInput: ExternalAgentSessionInput,
    stagedRuntimeId: string,
    generation: number,
  ) {
    if (generation !== deps.userScopeGeneration()) return
    deps.markSessionDeleted(draft.botId, created.id)
    deps.stopSessionRuntime(draft.botId, created.id)
    deps.removeSessionView(draft.botId, created.id)
    deps.clearRuntimeStatus(draft.botId, created.id)
    if (stagedRuntimeId) deps.clearRuntimeStatus(draft.botId, stagedRuntimeId)
    if ((deps.currentBotId.value ?? '').trim() === draft.botId) {
      deps.removeSessionFromList(created.id)
    }

    deps.forgetDraftStage(draft)
    deps.rememberDraftStage(draft, {
      botId: draft.botId,
      input: normalizedExternalAgentInput({ ...stagedInput, runtimeId: undefined }),
      runtimeId: '',
    })
    if (deps.isFocusedTarget(draft)) deps.activateDraftStage(draft)
    try {
      await deleteSession(draft.botId, created.id)
    } catch {
      // The local tombstone keeps a failed cleanup hidden until auth reset.
    }
  }

  async function createForTarget(
    input: ExternalAgentSessionInput,
    target: ChatViewTarget,
  ): Promise<{ session: SessionSummary }> {
    const draft = deps.targetDraftForExternalAgent(target)
    const generation = deps.userScopeGeneration()
    const stagedBeforeCreate = deps.pendingExternalAgentStateFor(draft)
    const runtimeId = input.runtimeId?.trim() ?? ''
    const created = await createExternalAgentSessionRecord(draft.botId, input)
    if (
      generation !== deps.userScopeGeneration()
      || (deps.currentBotId.value ?? '').trim() !== draft.botId
    ) {
      await rollbackFailedCreation(
        created,
        draft,
        stagedBeforeCreate?.input ?? input,
        runtimeId,
        generation,
      )
      const error = new Error('Chat scope changed during ACP Session creation')
      error.name = 'AbortError'
      throw error
    }

    deps.upsertSession(created)
    deps.rememberSession(created)
    if (runtimeId) deps.clearRuntimeStatus(draft.botId, runtimeId)
    deps.promoteDraftView(draft, created.id)
    if (stagedBeforeCreate) {
      if (runtimeId && stagedBeforeCreate.runtimeId === runtimeId) {
        deps.forgetDraftStage(draft)
      } else {
        deps.discardDraftStage(draft)
      }
    }
    if (deps.isFocusedTarget(draft)) {
      deps.sessionId.value = created.id
      deps.explicitSessionSelection.value = true
      deps.draftIntent.value = false
    }
    return { session: created }
  }

  async function createExternalAgentSession(input: ExternalAgentSessionInput) {
    const botId = deps.currentBotId.value ?? await deps.ensureBot()
    if (!botId) throw new Error('Bot not ready')
    return createForTarget(input, {
      botId,
      sessionId: null,
      viewId: deps.focusedViewId.value,
    })
  }

  async function updateCurrentSessionAgent(
    input: ExternalAgentSessionInput,
    target?: ChatViewTarget,
  ): Promise<{ session: SessionSummary }> {
    const resolved = deps.normalizeTarget(target)
    if (!resolved.sessionId) return createForTarget(input, resolved)
    const botId = resolved.botId
    const targetSessionId = resolved.sessionId
    if (!botId) throw new Error('Bot not selected')
    const runtimeType = agentSessionRuntimeType(input)
    const runtimeMetadata = externalAgentSessionMetadata(input)
    const current = deps.knownSessionSummary(targetSessionId)
    const sessionMode = current?.session_mode
      || (current?.type === 'discuss' ? 'discuss' : 'chat')
    const generation = deps.userScopeGeneration()
    const updated = await updateSessionAgent(botId, targetSessionId, {
      botAgentId: input.botAgentId,
      type: sessionMode,
      sessionMode,
      runtimeType,
      metadata: runtimeMetadata,
      runtimeMetadata,
    })
    if (
      generation !== deps.userScopeGeneration()
      || (deps.currentBotId.value ?? '').trim() !== botId
    ) return { session: updated }
    deps.upsertSession(updated)
    if (deps.isFocusedTarget(resolved)) {
      deps.explicitSessionSelection.value = true
      deps.draftIntent.value = false
    }
    deps.clearRuntimeStatus(botId, targetSessionId)
    return { session: updated }
  }

  async function updateCurrentSessionToMemoh(
    target?: ChatViewTarget,
  ): Promise<SessionSummary | null> {
    const resolved = deps.normalizeTarget(target)
    const botId = resolved.botId
    const targetSessionId = resolved.sessionId ?? ''
    if (!botId || !targetSessionId) return null
    const current = deps.knownSessionSummary(targetSessionId)
    const sessionMode = current?.session_mode
      || (current?.type === 'discuss' ? 'discuss' : 'chat')
    const generation = deps.userScopeGeneration()
    const updated = await updateSessionAgent(botId, targetSessionId, {
      botAgentId: '',
      type: sessionMode === 'discuss' ? 'discuss' : 'chat',
      sessionMode,
      runtimeType: 'model',
      metadata: {},
      runtimeMetadata: {},
    })
    if (
      generation !== deps.userScopeGeneration()
      || (deps.currentBotId.value ?? '').trim() !== botId
    ) return null
    deps.upsertSession(updated)
    if (deps.isFocusedTarget(resolved)) {
      deps.explicitSessionSelection.value = true
      deps.draftIntent.value = false
    }
    deps.clearRuntimeStatus(botId, targetSessionId)
    return updated
  }

  async function ensureChatViewSession(
    target: ChatViewTarget,
    firstPrompt?: string,
  ): Promise<ChatViewTarget> {
    if (target.sessionId) return target
    if (deps.isDraftCreationActive(target)) {
      throw new StreamFailureError('Session creation is already in progress', 'startup')
    }
    deps.beginDraftCreation(target)
    try {
      const pendingExternalAgent = deps.pendingExternalAgentStateFor(target)
      if (pendingExternalAgent) {
        const { session: created } = await createForTarget({
          ...pendingExternalAgent.input,
          runtimeId: pendingExternalAgent.runtimeId,
        }, target)
        if (firstPrompt?.trim() && !created.title?.trim()) {
          created.title = provisionalSessionTitle(firstPrompt)
          deps.upsertSession(created)
          deps.rememberSession(created)
        }
        return { ...target, sessionId: created.id }
      }

      const generation = deps.userScopeGeneration()
      const workdirId = deps.draftWorkdirIdFor(target.botId, { externalAgent: false })
      const created = await createSession(target.botId, {
        workdirId: workdirId || undefined,
      })
      if (
        generation !== deps.userScopeGeneration()
        || (deps.currentBotId.value ?? '').trim() !== target.botId
      ) {
        const error = new Error('Chat scope changed during Session creation')
        error.name = 'AbortError'
        throw error
      }
      if (firstPrompt?.trim()) created.title = provisionalSessionTitle(firstPrompt)
      deps.upsertSession(created)
      deps.rememberSession(created)
      deps.promoteDraftView(target, created.id)
      if (deps.isFocusedTarget(target)) {
        deps.sessionId.value = created.id
        deps.explicitSessionSelection.value = true
        deps.draftIntent.value = false
      }
      return { ...target, sessionId: created.id }
    } finally {
      deps.endDraftCreation(target)
    }
  }

  return {
    createExternalAgentSession,
    updateCurrentSessionAgent,
    updateCurrentSessionToMemoh,
    ensureChatViewSession,
  }
}
