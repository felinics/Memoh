import { ref } from 'vue'
import { externalAgentDraftMetadata, type DetachedExternalAgentSession, type createExternalAgentStaging } from './external-agent-staging'
import type { createACPRuntimeRegistry } from './acp-runtime-registry'
import type { ExternalAgentSessionInput, ChatViewTarget } from './types'
import type { ChatViewEntry } from './view-registry'

type ExternalAgentStaging = ReturnType<typeof createExternalAgentStaging>
type ACPRuntimeRegistry = ReturnType<typeof createACPRuntimeRegistry>

interface DraftExternalAgentStage extends DetachedExternalAgentSession {
  viewId: string
}

export interface ExternalAgentOrchestrationDeps {
  staging: ExternalAgentStaging
  runtimeRegistry: ACPRuntimeRegistry
  normalizeTarget: (target?: Partial<ChatViewTarget>) => ChatViewTarget
  invalidateDraftCommand: (target: ChatViewTarget) => void
  forgetDraftCommand: (target: ChatViewTarget) => void
  resetWorkspaceTargetSelection: (target: ChatViewTarget) => void
}

function draftStageKey(botId: string, viewId: string) {
  return `${botId.trim()}\u0000${viewId.trim()}`
}

export function createExternalAgentOrchestration(deps: ExternalAgentOrchestrationDeps) {
  const {
    pendingExternalAgentSessionInput,
    pendingACPRuntimeId,
    pendingExternalAgentSessionMetadata,
    pendingACPRuntimeStatus,
    pendingACPRuntimeEnsuring,
    stageExternalAgentSession: stageFocusedExternalAgentSession,
    stageDefaultExternalAgentSession: stageFocusedDefaultExternalAgentSession,
    stageNewExternalAgentSession: stageFocusedNewExternalAgentSession,
    resetToEmptyComposer: resetFocusedEmptyComposer,
    ensurePendingACPRuntime: ensureFocusedPendingACPRuntime,
    setPendingACPModel: setFocusedPendingACPModel,
    setPendingACPMode: setFocusedPendingACPMode,
    setPendingACPReasoning: setFocusedPendingACPReasoning,
    detachPendingExternalAgentSession,
    restorePendingExternalAgentSession,
    releasePendingExternalAgentSession,
    discardDetachedExternalAgentSession,
    pendingExternalAgentMatchesInput: focusedPendingExternalAgentMatchesInput,
  } = deps.staging
  const {
    acpRuntimeStatuses,
    acpRuntimeKey,
  } = deps.runtimeRegistry

  const draftStages = ref<Record<string, DraftExternalAgentStage>>({})
  let liveDraft: { botId: string; viewId: string } | null = null

  function isLiveDraft(
    left: { botId: string; viewId: string } | null,
    right: ChatViewTarget,
  ) {
    return !!left
      && left.botId === right.botId.trim()
      && left.viewId === right.viewId.trim()
      && !right.sessionId
  }

  function rememberDraftStage(
    target: Pick<ChatViewTarget, 'botId' | 'viewId'>,
    detached: DetachedExternalAgentSession,
  ) {
    const key = draftStageKey(target.botId, target.viewId)
    draftStages.value = {
      ...draftStages.value,
      [key]: {
        botId: detached.botId.trim() || target.botId.trim(),
        viewId: target.viewId.trim(),
        input: { ...detached.input },
        runtimeId: detached.runtimeId.trim(),
      },
    }
  }

  function syncLiveDraftStage() {
    if (!liveDraft || !pendingExternalAgentSessionInput.value) return
    rememberDraftStage(liveDraft, {
      botId: liveDraft.botId,
      input: pendingExternalAgentSessionInput.value,
      runtimeId: pendingACPRuntimeId.value,
    })
  }

  function saveLiveDraftStage() {
    if (!liveDraft) return
    const owner = liveDraft
    const detached = detachPendingExternalAgentSession()
    if (detached) rememberDraftStage(owner, detached)
    liveDraft = null
  }

  function activateDraftStage(target: ChatViewTarget) {
    const resolved = deps.normalizeTarget(target)
    if (resolved.sessionId || !resolved.botId || !resolved.viewId) return
    if (isLiveDraft(liveDraft, resolved)) return
    saveLiveDraftStage()
    liveDraft = { botId: resolved.botId, viewId: resolved.viewId }
    const saved = draftStages.value[draftStageKey(resolved.botId, resolved.viewId)]
    if (saved) restorePendingExternalAgentSession(saved.input, saved.runtimeId, saved.botId)
    else releasePendingExternalAgentSession()
  }

  function forgetDraftStage(target: ChatViewTarget) {
    const resolved = deps.normalizeTarget(target)
    const key = draftStageKey(resolved.botId, resolved.viewId)
    if (isLiveDraft(liveDraft, resolved)) {
      releasePendingExternalAgentSession()
      liveDraft = null
    }
    if (!(key in draftStages.value)) return
    const { [key]: _removed, ...rest } = draftStages.value
    draftStages.value = rest
  }

  function discardDraftStage(target: ChatViewTarget) {
    const resolved = deps.normalizeTarget(target)
    const key = draftStageKey(resolved.botId, resolved.viewId)
    if (isLiveDraft(liveDraft, resolved)) {
      deps.staging.clearPendingExternalAgentSession()
      liveDraft = null
    } else {
      const saved = draftStages.value[key]
      if (saved) discardDetachedExternalAgentSession(saved)
    }
    if (!(key in draftStages.value)) return
    const { [key]: _removed, ...rest } = draftStages.value
    draftStages.value = rest
  }

  function discardEvictedDraft(view: ChatViewEntry) {
    const target = { botId: view.botId, sessionId: null, viewId: view.viewId }
    deps.forgetDraftCommand(target)
    discardDraftStage(target)
  }

  function pendingExternalAgentStateFor(target: ChatViewTarget) {
    const resolved = deps.normalizeTarget(target)
    if (resolved.sessionId) return null
    const live = isLiveDraft(liveDraft, resolved)
    const saved = live && pendingExternalAgentSessionInput.value
      ? {
          botId: liveDraft!.botId,
          viewId: liveDraft!.viewId,
          input: pendingExternalAgentSessionInput.value,
          runtimeId: pendingACPRuntimeId.value,
        }
      : draftStages.value[draftStageKey(resolved.botId, resolved.viewId)]
    if (!saved) return null
    const runtimeKey = acpRuntimeKey(saved.botId, saved.runtimeId)
    return {
      input: { ...saved.input },
      metadata: externalAgentDraftMetadata(saved.input),
      runtimeId: saved.runtimeId,
      runtimeStatus: runtimeKey ? acpRuntimeStatuses.value[runtimeKey] : undefined,
      ensuring: live ? pendingACPRuntimeEnsuring.value : false,
    }
  }

  function targetDraft(target?: ChatViewTarget): ChatViewTarget {
    const resolved = deps.normalizeTarget(target)
    return { ...resolved, sessionId: null }
  }

  function stageExternalAgentSession(
    input: ExternalAgentSessionInput,
    options: { explicitSelection?: boolean } = {},
    target?: ChatViewTarget,
  ) {
    const draft = targetDraft(target)
    deps.invalidateDraftCommand(draft)
    activateDraftStage(draft)
    stageFocusedExternalAgentSession(input, options)
    syncLiveDraftStage()
  }

  function stageDefaultExternalAgentSession(input: ExternalAgentSessionInput, target?: ChatViewTarget) {
    const draft = targetDraft(target)
    deps.invalidateDraftCommand(draft)
    activateDraftStage(draft)
    stageFocusedDefaultExternalAgentSession(input)
    syncLiveDraftStage()
  }

  function stageNewExternalAgentSession(input: ExternalAgentSessionInput, target?: ChatViewTarget) {
    const draft = targetDraft(target)
    deps.invalidateDraftCommand(draft)
    activateDraftStage(draft)
    stageFocusedNewExternalAgentSession(input)
    syncLiveDraftStage()
  }

  function resetToEmptyComposer(
    options: {
      clearPendingExternalAgent?: boolean
      explicitSelection?: boolean
      draftIntent?: boolean
    } = {},
    target?: ChatViewTarget,
  ) {
    const draft = targetDraft(target)
    deps.resetWorkspaceTargetSelection(draft)
    deps.invalidateDraftCommand(draft)
    activateDraftStage(draft)
    resetFocusedEmptyComposer(options)
    if (options.clearPendingExternalAgent !== false) forgetDraftStage(draft)
  }

  async function ensurePendingACPRuntime(target?: ChatViewTarget) {
    const draft = targetDraft(target)
    activateDraftStage(draft)
    try {
      return await ensureFocusedPendingACPRuntime()
    } finally {
      syncLiveDraftStage()
    }
  }

  async function setPendingACPModel(modelId: string, target?: ChatViewTarget) {
    const draft = targetDraft(target)
    deps.invalidateDraftCommand(draft)
    activateDraftStage(draft)
    try {
      return await setFocusedPendingACPModel(modelId)
    } finally {
      syncLiveDraftStage()
    }
  }

  async function setPendingACPReasoning(effort: string, target?: ChatViewTarget) {
    const draft = targetDraft(target)
    deps.invalidateDraftCommand(draft)
    activateDraftStage(draft)
    try {
      return await setFocusedPendingACPReasoning(effort)
    } finally {
      syncLiveDraftStage()
    }
  }

  async function setPendingACPMode(modeId: string, target?: ChatViewTarget) {
    const draft = targetDraft(target)
    deps.invalidateDraftCommand(draft)
    activateDraftStage(draft)
    try {
      return await setFocusedPendingACPMode(modeId)
    } finally {
      syncLiveDraftStage()
    }
  }

  function pendingExternalAgentMatchesInput(input: ExternalAgentSessionInput, target?: ChatViewTarget) {
    if (!target) return focusedPendingExternalAgentMatchesInput(input)
    const state = pendingExternalAgentStateFor(target)
    if (!state) return false
    const metadata = externalAgentDraftMetadata(input)
    return state.metadata.acp_agent_id === metadata.acp_agent_id
      && (state.input.botAgentId ?? '') === (input.botAgentId?.trim() ?? '')
      && (state.input.runtime || 'acp') === (input.runtime || 'acp')
      && state.metadata.project_path === metadata.project_path
      && state.metadata.acp_project_mode === metadata.acp_project_mode
  }

  function reset() {
    draftStages.value = {}
    liveDraft = null
  }

  return {
    pendingExternalAgentSessionInput,
    pendingACPRuntimeId,
    pendingExternalAgentSessionMetadata,
    pendingACPRuntimeStatus,
    pendingACPRuntimeEnsuring,
    pendingExternalAgentStateFor,
    targetDraftForExternalAgent: targetDraft,
    stageExternalAgentSession,
    stageDefaultExternalAgentSession,
    stageNewExternalAgentSession,
    resetToEmptyComposer,
    ensurePendingACPRuntime,
    setPendingACPModel,
    setPendingACPMode,
    setPendingACPReasoning,
    pendingExternalAgentMatchesInput,
    sameDraftExternalAgentStage: isLiveDraft,
    rememberDraftExternalAgentStage: rememberDraftStage,
    saveLiveDraftExternalAgentStage: saveLiveDraftStage,
    activateDraftExternalAgentStage: activateDraftStage,
    forgetDraftExternalAgentStage: forgetDraftStage,
    discardDraftExternalAgentStage: discardDraftStage,
    discardEvictedDraft,
    releasePendingExternalAgentSession,
    reset,
  }
}
