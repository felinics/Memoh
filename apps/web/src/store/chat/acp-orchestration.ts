import { ref } from 'vue'
import { acpSessionMetadata, type DetachedACPSession, type createACPStaging } from './acp-staging'
import type { createACPRuntimeRegistry } from './acp-runtime-registry'
import type { ACPAgentSessionInput, ChatViewTarget } from './types'
import type { ChatViewEntry } from './view-registry'

type ACPStaging = ReturnType<typeof createACPStaging>
type ACPRuntimeRegistry = ReturnType<typeof createACPRuntimeRegistry>

interface DraftACPStage extends DetachedACPSession {
  viewId: string
}

export interface ACPOrchestrationDeps {
  staging: ACPStaging
  runtimeRegistry: ACPRuntimeRegistry
  normalizeTarget: (target?: Partial<ChatViewTarget>) => ChatViewTarget
  invalidateDraftCommand: (target: ChatViewTarget) => void
  forgetDraftCommand: (target: ChatViewTarget) => void
  resetWorkspaceTargetSelection: (target: ChatViewTarget) => void
  shouldPrewarmDraftACP: (target: ChatViewTarget) => boolean
  // The project path the draft's Folder binding dictates ('' when the draft
  // has no Folder or the Folder must not prewarm). A prewarmed runtime is
  // only reusable when it was created on this exact path.
  draftProjectPathFor?: (target: ChatViewTarget) => string
}

function draftStageKey(botId: string, viewId: string) {
  return `${botId.trim()}\u0000${viewId.trim()}`
}

export function createACPOrchestration(deps: ACPOrchestrationDeps) {
  const {
    pendingACPSessionInput,
    pendingACPRuntimeId,
    pendingACPSessionMetadata,
    pendingACPRuntimeStatus,
    pendingACPRuntimeEnsuring,
    stageACPSession: stageFocusedACPSession,
    stageDefaultACPSession: stageFocusedDefaultACPSession,
    stageNewACPSession: stageFocusedNewACPSession,
    resetToEmptyComposer: resetFocusedEmptyComposer,
    ensurePendingACPRuntime: ensureFocusedPendingACPRuntime,
    setPendingACPModel: setFocusedPendingACPModel,
    setPendingACPMode: setFocusedPendingACPMode,
    setPendingACPReasoning: setFocusedPendingACPReasoning,
    discardPendingACPRuntime: discardFocusedPendingACPRuntime,
    detachPendingACPSession,
    restorePendingACPSession,
    releasePendingACPSession,
    discardDetachedACPSession,
    pendingACPMatchesInput: focusedPendingACPMatchesInput,
  } = deps.staging
  const {
    acpRuntimeStatuses,
    acpRuntimeKey,
  } = deps.runtimeRegistry

  const draftStages = ref<Record<string, DraftACPStage>>({})
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
    detached: DetachedACPSession,
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
    if (!liveDraft || !pendingACPSessionInput.value) return
    rememberDraftStage(liveDraft, {
      botId: liveDraft.botId,
      input: pendingACPSessionInput.value,
      runtimeId: pendingACPRuntimeId.value,
    })
  }

  function saveLiveDraftStage() {
    if (!liveDraft) return
    const owner = liveDraft
    const detached = detachPendingACPSession()
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
    if (saved) restorePendingACPSession(saved.input, saved.runtimeId, saved.botId)
    else releasePendingACPSession()
  }

  function forgetDraftStage(target: ChatViewTarget) {
    const resolved = deps.normalizeTarget(target)
    const key = draftStageKey(resolved.botId, resolved.viewId)
    if (isLiveDraft(liveDraft, resolved)) {
      releasePendingACPSession()
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
      deps.staging.clearPendingACPSession()
      liveDraft = null
    } else {
      const saved = draftStages.value[key]
      if (saved) discardDetachedACPSession(saved)
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

  function pendingACPStateFor(target: ChatViewTarget) {
    const resolved = deps.normalizeTarget(target)
    if (resolved.sessionId) return null
    const live = isLiveDraft(liveDraft, resolved)
    const saved = live && pendingACPSessionInput.value
      ? {
          botId: liveDraft!.botId,
          viewId: liveDraft!.viewId,
          input: pendingACPSessionInput.value,
          runtimeId: pendingACPRuntimeId.value,
        }
      : draftStages.value[draftStageKey(resolved.botId, resolved.viewId)]
    if (!saved) return null
    const runtimeKey = acpRuntimeKey(saved.botId, saved.runtimeId)
    return {
      input: { ...saved.input },
      metadata: acpSessionMetadata(saved.input),
      runtimeId: saved.runtimeId,
      runtimeStatus: runtimeKey ? acpRuntimeStatuses.value[runtimeKey] : undefined,
      ensuring: live ? pendingACPRuntimeEnsuring.value : false,
    }
  }

  function targetDraft(target?: ChatViewTarget): ChatViewTarget {
    const resolved = deps.normalizeTarget(target)
    return { ...resolved, sessionId: null }
  }

  function stageACPSession(
    input: ACPAgentSessionInput,
    options: { explicitSelection?: boolean } = {},
    target?: ChatViewTarget,
  ) {
    const draft = targetDraft(target)
    deps.invalidateDraftCommand(draft)
    activateDraftStage(draft)
    stageFocusedACPSession(input, options)
    syncLiveDraftStage()
  }

  function stageDefaultACPSession(input: ACPAgentSessionInput, target?: ChatViewTarget) {
    const draft = targetDraft(target)
    deps.invalidateDraftCommand(draft)
    activateDraftStage(draft)
    stageFocusedDefaultACPSession(input)
    syncLiveDraftStage()
  }

  function stageNewACPSession(input: ACPAgentSessionInput, target?: ChatViewTarget) {
    const draft = targetDraft(target)
    deps.invalidateDraftCommand(draft)
    activateDraftStage(draft)
    stageFocusedNewACPSession(input)
    syncLiveDraftStage()
  }

  function resetToEmptyComposer(
    options: {
      clearPendingACP?: boolean
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
    if (options.clearPendingACP !== false) forgetDraftStage(draft)
  }

  // A Folder binding dictates the runtime's project path. Re-staging with the
  // Folder's path changes the staging identity, so a runtime warmed on
  // another path is closed instead of being silently reused for the Folder.
  function alignFocusedDraftProjectPath(draft: ChatViewTarget) {
    const folderPath = deps.draftProjectPathFor?.(draft) ?? ''
    if (!folderPath) return
    const pending = pendingACPSessionInput.value
    if (!pending || (pending.projectPath ?? '').trim() === folderPath) return
    // Deliberately marks the selection explicit (stageACPSession's default):
    // the Folder-derived path must not be overwritten by the default-ACP
    // re-stage watcher, which would oscillate with this alignment forever.
    stageFocusedACPSession({ ...pending, projectPath: folderPath })
  }

  async function ensurePendingACPRuntime(target?: ChatViewTarget) {
    const draft = targetDraft(target)
    activateDraftStage(draft)
    try {
      if (!deps.shouldPrewarmDraftACP(draft)) {
        discardFocusedPendingACPRuntime()
        return undefined
      }
      alignFocusedDraftProjectPath(draft)
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
      if (!deps.shouldPrewarmDraftACP(draft)) {
        discardFocusedPendingACPRuntime()
        return undefined
      }
      alignFocusedDraftProjectPath(draft)
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
      if (!deps.shouldPrewarmDraftACP(draft)) {
        discardFocusedPendingACPRuntime()
        return undefined
      }
      alignFocusedDraftProjectPath(draft)
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
      // Same suppression as ensure/model/reasoning: /permission on a pending
      // draft must not resurrect or mutate a Primary runtime for a draft
      // whose Folder pins another computer.
      if (!deps.shouldPrewarmDraftACP(draft)) {
        discardFocusedPendingACPRuntime()
        return undefined
      }
      alignFocusedDraftProjectPath(draft)
      return await setFocusedPendingACPMode(modeId)
    } finally {
      syncLiveDraftStage()
    }
  }

  function pendingACPMatchesInput(input: ACPAgentSessionInput, target?: ChatViewTarget) {
    if (!target) return focusedPendingACPMatchesInput(input)
    const state = pendingACPStateFor(target)
    if (!state) return false
    const metadata = acpSessionMetadata(input)
    return state.metadata.acp_agent_id === metadata.acp_agent_id
      && state.metadata.project_path === metadata.project_path
      && state.metadata.acp_project_mode === metadata.acp_project_mode
  }

  function pendingACPStateForSession(target: ChatViewTarget) {
    const draft = targetDraft(target)
    const state = pendingACPStateFor(draft)
    if (!state || deps.shouldPrewarmDraftACP(draft)) return state

    activateDraftStage(draft)
    discardFocusedPendingACPRuntime()
    syncLiveDraftStage()
    return pendingACPStateFor(draft)
  }

  function reset() {
    draftStages.value = {}
    liveDraft = null
  }

  return {
    pendingACPSessionInput,
    pendingACPRuntimeId,
    pendingACPSessionMetadata,
    pendingACPRuntimeStatus,
    pendingACPRuntimeEnsuring,
    pendingACPStateFor,
    pendingACPStateForSession,
    targetDraftForACP: targetDraft,
    stageACPSession,
    stageDefaultACPSession,
    stageNewACPSession,
    resetToEmptyComposer,
    ensurePendingACPRuntime,
    setPendingACPModel,
    setPendingACPMode,
    setPendingACPReasoning,
    pendingACPMatchesInput,
    sameDraftACPStage: isLiveDraft,
    rememberDraftACPStage: rememberDraftStage,
    saveLiveDraftACPStage: saveLiveDraftStage,
    activateDraftACPStage: activateDraftStage,
    forgetDraftACPStage: forgetDraftStage,
    discardDraftACPStage: discardDraftStage,
    discardEvictedDraft,
    releasePendingACPSession,
    reset,
  }
}
