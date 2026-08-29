import type { Ref } from 'vue'
import {
  createSession,
  deleteSession,
  updateSessionAgent,
  type SessionSummary,
} from '@/composables/api/useChat'
import { provisionalSessionTitle } from '../chat-list.utils'
import { acpSessionMetadata } from './acp-staging'
import { StreamFailureError } from './send'
import type { ACPAgentSessionInput, ChatViewTarget } from './types'

interface PendingACPState {
  input: ACPAgentSessionInput
  runtimeId: string
}

export interface ACPSessionDeps {
  currentBotId: Ref<string | null>
  sessionId: Ref<string | null>
  draftIntent: Ref<boolean>
  explicitSessionSelection: Ref<boolean>
  focusedViewId: Ref<string>
  userScopeGeneration: () => number
  normalizeTarget: (target?: Partial<ChatViewTarget>) => ChatViewTarget
  targetDraftForACP: (target?: ChatViewTarget) => ChatViewTarget
  pendingACPStateFor: (target: ChatViewTarget) => PendingACPState | null
  isFocusedTarget: (target: ChatViewTarget) => boolean
  upsertSession: (session: SessionSummary) => void
  rememberSession: (session: SessionSummary) => void
  promoteDraftView: (target: ChatViewTarget, sessionId: string) => void
  clearRuntimeStatus: (botId: string, runtimeId: string) => void
  forgetDraftStage: (target: ChatViewTarget) => void
  discardDraftStage: (target: ChatViewTarget) => void
  rememberDraftStage: (
    target: ChatViewTarget,
    detached: { botId: string; input: ACPAgentSessionInput; runtimeId: string },
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
  // The synchronous value conservatively retains a persisted selection so ACP
  // prewarm can be suppressed while the list loads. Creation uses the async
  // resolver to validate the immutable binding first.
  draftWorkdirBindingFor: (botId: string) => { id: string, kind: string, path: string }
  resolveDraftWorkdirIdFor: (botId: string) => Promise<string>
}

function normalizedACPInput(input: ACPAgentSessionInput): ACPAgentSessionInput {
  const metadata = acpSessionMetadata(input)
  return {
    ...input,
    agentId: String(metadata.acp_agent_id ?? ''),
    projectPath: String(metadata.project_path ?? ''),
    projectMode: String(metadata.acp_project_mode ?? ''),
  }
}

export function createACPSessions(deps: ACPSessionDeps) {
  async function createACPSessionRecord(
    botId: string,
    input: ACPAgentSessionInput,
    workdirId: string,
  ): Promise<SessionSummary> {
    const id = botId.trim()
    if (!id) throw new Error('Bot not ready')
    const metadata = acpSessionMetadata(input)
    const runtimeId = input.runtimeId?.trim() ?? ''
    const sessionMode = input.sessionMode === 'discuss' ? 'discuss' : 'chat'
    return createSession(id, {
      botAgentId: input.botAgentId,
      title: input.title ?? '',
      type: sessionMode,
      sessionMode,
      runtimeType: 'acp_agent',
      metadata: {},
      runtimeMetadata: metadata,
      acpRuntimeId: runtimeId || undefined,
      workdirId: workdirId || undefined,
    })
  }

  async function rollbackFailedCreation(
    created: SessionSummary,
    draft: ChatViewTarget,
    stagedInput: ACPAgentSessionInput,
    stagedRuntimeId: string,
    droppedRuntimeId: string,
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

    // A staged runtime that was not handed to the session must be closed, not
    // merely forgotten — otherwise it idles server-side until the reaper.
    if (droppedRuntimeId) deps.discardDraftStage(draft)
    else deps.forgetDraftStage(draft)
    deps.rememberDraftStage(draft, {
      botId: draft.botId,
      input: normalizedACPInput({ ...stagedInput, runtimeId: undefined }),
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
    input: ACPAgentSessionInput,
    target: ChatViewTarget,
  ): Promise<{ session: SessionSummary }> {
    const draft = deps.targetDraftForACP(target)
    const generation = deps.userScopeGeneration()
    const stagedBeforeCreate = deps.pendingACPStateFor(draft)
    const workdirId = await deps.resolveDraftWorkdirIdFor(draft.botId)
    // A native Folder shares Primary's workspace, so a runtime prewarmed on
    // the Folder's path is reusable — the backend still refuses the bind if
    // the paths disagree. Any other binding may target a different computer:
    // never hand it a prewarmed Primary runtime.
    const binding = workdirId ? deps.draftWorkdirBindingFor(draft.botId) : null
    const reusableFolderRuntime = !!binding
      && binding.id === workdirId
      && binding.kind === 'native'
      && !!binding.path
      && (input.projectPath ?? '').trim() === binding.path
    const runtimeId = workdirId && !reusableFolderRuntime ? '' : input.runtimeId?.trim() ?? ''
    const created = await createACPSessionRecord(draft.botId, {
      ...input,
      runtimeId: runtimeId || undefined,
    }, workdirId)
    if (
      generation !== deps.userScopeGeneration()
      || (deps.currentBotId.value ?? '').trim() !== draft.botId
    ) {
      await rollbackFailedCreation(
        created,
        draft,
        stagedBeforeCreate?.input ?? input,
        runtimeId,
        stagedBeforeCreate?.runtimeId && stagedBeforeCreate.runtimeId !== runtimeId
          ? stagedBeforeCreate.runtimeId
          : '',
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

  async function createACPSession(input: ACPAgentSessionInput) {
    const botId = deps.currentBotId.value ?? await deps.ensureBot()
    if (!botId) throw new Error('Bot not ready')
    return createForTarget(input, {
      botId,
      sessionId: null,
      viewId: deps.focusedViewId.value,
    })
  }

  async function updateCurrentSessionAgent(
    input: ACPAgentSessionInput,
    target?: ChatViewTarget,
  ): Promise<{ session: SessionSummary }> {
    const resolved = deps.normalizeTarget(target)
    if (!resolved.sessionId) return createForTarget(input, resolved)
    const botId = resolved.botId
    const targetSessionId = resolved.sessionId
    if (!botId) throw new Error('Bot not selected')
    const metadata = acpSessionMetadata(input)
    const current = deps.knownSessionSummary(targetSessionId)
    const sessionMode = current?.session_mode
      || (current?.type === 'discuss' ? 'discuss' : 'chat')
    const generation = deps.userScopeGeneration()
    const updated = await updateSessionAgent(botId, targetSessionId, {
      botAgentId: input.botAgentId,
      type: sessionMode === 'discuss' ? 'discuss' : 'acp_agent',
      sessionMode,
      runtimeType: 'acp_agent',
      metadata,
      runtimeMetadata: metadata,
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
      const pendingACP = deps.pendingACPStateFor(target)
      if (pendingACP) {
        const { session: created } = await createForTarget({
          ...pendingACP.input,
          runtimeId: pendingACP.runtimeId,
        }, target)
        if (firstPrompt?.trim() && !created.title?.trim()) {
          created.title = provisionalSessionTitle(firstPrompt)
          deps.upsertSession(created)
          deps.rememberSession(created)
        }
        return { ...target, sessionId: created.id }
      }

      const generation = deps.userScopeGeneration()
      const workdirId = await deps.resolveDraftWorkdirIdFor(target.botId)
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
    createACPSession,
    updateCurrentSessionAgent,
    updateCurrentSessionToMemoh,
    ensureChatViewSession,
  }
}
