import type { Ref } from 'vue'
import {
  createSession,
  deleteSession,
  type SessionSummary,
} from '@/composables/api/useChat'
import { provisionalSessionTitle } from '../chat-list.utils'
import { externalAgentDraftMetadata, normalizedExternalAgentInput } from './external-agent-staging'
import { StreamFailureError } from './send'
import type { ExternalAgentSessionInput, ChatViewTarget } from './types'

interface PendingExternalAgentState {
  input: ExternalAgentSessionInput
}

export interface ExternalAgentSessionDeps {
  currentBotId: Ref<string | null>
  sessionId: Ref<string | null>
  draftIntent: Ref<boolean>
  explicitSessionSelection: Ref<boolean>
  focusedViewId: Ref<string>
  userScopeGeneration: () => number
  targetDraftForExternalAgent: (target?: ChatViewTarget) => ChatViewTarget
  pendingExternalAgentStateFor: (target: ChatViewTarget) => PendingExternalAgentState | null
  isFocusedTarget: (target: ChatViewTarget) => boolean
  upsertSession: (session: SessionSummary) => void
  rememberSession: (session: SessionSummary) => void
  promoteDraftView: (target: ChatViewTarget, sessionId: string) => void
  clearRuntimeStatus: (botId: string, runtimeId: string) => void
  forgetDraftStage: (target: ChatViewTarget) => void
  rememberDraftStage: (
    target: ChatViewTarget,
    detached: { botId: string; input: ExternalAgentSessionInput },
  ) => void
  activateDraftStage: (target: ChatViewTarget) => void
  markSessionDeleted: (botId: string, sessionId: string) => void
  stopSessionRuntime: (botId: string, sessionId: string) => void
  removeSessionView: (botId: string, sessionId: string) => void
  removeSessionFromList: (sessionId: string) => void
  ensureBot: () => Promise<string | null>
  isDraftCreationActive: (target: ChatViewTarget) => boolean
  beginDraftCreation: (target: ChatViewTarget) => void
  endDraftCreation: (target: ChatViewTarget) => void
  // Resolves the bot's working workdir for a new session ('' = no binding).
  // External Agent sessions can only bind native-workspace workdirs, so the resolver is
  // told which runtime the session will use.
  draftWorkdirIdFor: (botId: string, opts: { externalAgent: boolean }) => string
}

function agentSessionRuntimeType(input: ExternalAgentSessionInput): string {
  const runtime = normalizedExternalAgentInput(input).runtime
  return runtime === 'acp' ? 'acp_agent' : runtime
}

export function externalAgentSessionMetadata(input: ExternalAgentSessionInput): Record<string, unknown> {
  const acp = agentSessionRuntimeType(input) === 'acp_agent'
  const botAgentId = input.botAgentId?.trim()
  return {
    ...(acp ? externalAgentDraftMetadata(input) : {}),
    // The Agent instance names the ACP profile. A request with none names the
    // profile, and the server binds the session to the bot's instance of it.
    ...(acp && !botAgentId ? { acp_agent_id: input.agentId.trim() } : {}),
    // The agent picks these up when the session's first process starts.
    ...(acp && input.acpModelId ? { acp_model_id: input.acpModelId } : {}),
    ...(acp && input.acpReasoningEffort ? { acp_reasoning_effort: input.acpReasoningEffort } : {}),
    ...(input.permissionMode ? { permission_mode: input.permissionMode } : {}),
    ...(input.planMode !== undefined ? { collaboration_mode: input.planMode ? 'plan' : 'default' } : {}),
  }
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
      workdirId: workdirId || undefined,
    })
  }

  async function rollbackFailedCreation(
    created: SessionSummary,
    draft: ChatViewTarget,
    stagedInput: ExternalAgentSessionInput,
    generation: number,
  ) {
    if (generation !== deps.userScopeGeneration()) return
    deps.markSessionDeleted(draft.botId, created.id)
    deps.stopSessionRuntime(draft.botId, created.id)
    deps.removeSessionView(draft.botId, created.id)
    deps.clearRuntimeStatus(draft.botId, created.id)
    if ((deps.currentBotId.value ?? '').trim() === draft.botId) {
      deps.removeSessionFromList(created.id)
    }

    deps.forgetDraftStage(draft)
    deps.rememberDraftStage(draft, {
      botId: draft.botId,
      input: normalizedExternalAgentInput(stagedInput),
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
    const created = await createExternalAgentSessionRecord(draft.botId, input)
    if (
      generation !== deps.userScopeGeneration()
      || (deps.currentBotId.value ?? '').trim() !== draft.botId
    ) {
      await rollbackFailedCreation(
        created,
        draft,
        stagedBeforeCreate?.input ?? input,
        generation,
      )
      const error = new Error('Chat scope changed during external-agent session creation')
      error.name = 'AbortError'
      throw error
    }

    deps.upsertSession(created)
    deps.rememberSession(created)
    deps.promoteDraftView(draft, created.id)
    if (stagedBeforeCreate) deps.forgetDraftStage(draft)
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

  // Creates the session a pending External Agent draft sends into. Native
  // drafts never come here: their first message creates the session in-band
  // (see send.ts), so the message path owns their workdir and model pair.
  async function ensureChatViewSession(
    target: ChatViewTarget,
    firstPrompt?: string,
  ): Promise<ChatViewTarget> {
    if (target.sessionId) return target
    const pendingExternalAgent = deps.pendingExternalAgentStateFor(target)
    if (!pendingExternalAgent) {
      throw new StreamFailureError('Session not selected', 'startup')
    }
    if (deps.isDraftCreationActive(target)) {
      throw new StreamFailureError('Session creation is already in progress', 'startup')
    }
    deps.beginDraftCreation(target)
    try {
      const { session: created } = await createForTarget(pendingExternalAgent.input, target)
      if (firstPrompt?.trim() && !created.title?.trim()) {
        created.title = provisionalSessionTitle(firstPrompt)
        deps.upsertSession(created)
        deps.rememberSession(created)
      }
      return { ...target, sessionId: created.id }
    } finally {
      deps.endDraftCreation(target)
    }
  }

  return {
    createExternalAgentSession,
    ensureChatViewSession,
  }
}
