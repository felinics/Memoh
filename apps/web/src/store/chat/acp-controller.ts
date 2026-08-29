import { ref, type Ref } from 'vue'
import { acpSessionMetadata, createACPStaging } from './acp-staging'
import { createACPOrchestration } from './acp-orchestration'
import { createACPRuntimeRegistry } from './acp-runtime-registry'
import { createACPSessions } from './acp-sessions'
import { createACPDefaults } from './acp-defaults'
import type { ChatViewEntry } from './view-registry'
import type {
  ACPAgentSessionInput,
  ChatViewTarget,
} from './types'
import type { SessionSummary } from '@/composables/api/useChat'

export function createACPController(deps: {
  currentBotId: Ref<string | null>
  sessionId: Ref<string | null>
  draftIntent: Ref<boolean>
  explicitSessionSelection: Ref<boolean>
  focusedViewId: Ref<string>
  userScopeGeneration: () => number
  bumpSelectSessionRequest: () => number
  currentSelectSessionRequest: () => number
  normalizeTarget: (target?: Partial<ChatViewTarget>) => ChatViewTarget
  isFocusedTarget: (target: ChatViewTarget) => boolean
  chatView: (target?: Partial<ChatViewTarget>) => ChatViewEntry
  draftCreationKey: (target: ChatViewTarget) => string
  draftSessionCreations: Set<string>
  stopSessionRuntime: (botId: string, sessionId: string) => void
  clearHistoryView: () => void
  resetWorkspaceTargetSelection: (target: ChatViewTarget) => void
  upsertSession: (session: SessionSummary) => void
  rememberSession: (session: SessionSummary) => void
  promoteDraftView: (target: ChatViewTarget, sessionId: string) => ChatViewEntry
  markSessionDeleted: (botId: string, sessionId: string) => void
  removeSessionView: (botId: string, sessionId: string) => void
  removeSessionFromList: (sessionId: string) => void
  ensureBot: () => Promise<string | null>
  knownSession: (sessionId: string) => SessionSummary | null | undefined
  draftWorkdirBindingFor: (botId: string) => { id: string, kind: string, path: string }
  resolveDraftWorkdirIdFor: (botId: string) => Promise<string>
}) {
  const runtimeRegistry = createACPRuntimeRegistry({
    currentBotId: deps.currentBotId,
    sessionId: deps.sessionId,
  })
  const staging = createACPStaging({
    currentBotId: deps.currentBotId,
    sessionId: deps.sessionId,
    draftIntent: deps.draftIntent,
    explicitSessionSelection: deps.explicitSessionSelection,
    runtimeRegistry,
    bumpSelectSessionRequest: deps.bumpSelectSessionRequest,
    clearTranscriptForDraft: () => {
      const botId = (deps.currentBotId.value ?? '').trim()
      const sessionId = (deps.sessionId.value ?? '').trim()
      if (botId && sessionId) deps.stopSessionRuntime(botId, sessionId)
      deps.clearHistoryView()
    },
  })

  const draftViewCommandVersions = new Map<string, number>()
  let draftViewCommandSequence = 0
  function invalidateDraftViewCommand(target: ChatViewTarget) {
    draftViewCommandVersions.delete(deps.draftCreationKey(target))
  }
  function beginDraftViewCommand(target: ChatViewTarget) {
    const key = deps.draftCreationKey(target)
    const version = ++draftViewCommandSequence
    draftViewCommandVersions.set(key, version)
    return {
      isCurrent: () => draftViewCommandVersions.get(key) === version,
      finish: () => {
        if (draftViewCommandVersions.get(key) === version) {
          draftViewCommandVersions.delete(key)
        }
      },
    }
  }

  const orchestration = createACPOrchestration({
    staging,
    runtimeRegistry,
    normalizeTarget: deps.normalizeTarget,
    invalidateDraftCommand: invalidateDraftViewCommand,
    forgetDraftCommand: target => {
      draftViewCommandVersions.delete(deps.draftCreationKey(target))
    },
    resetWorkspaceTargetSelection: deps.resetWorkspaceTargetSelection,
    // A native Folder shares the workspace every chat already uses, so the
    // draft may prewarm there (on the Folder's path) and keep the model and
    // reasoning pickers live. A remote Folder — or one whose kind is not yet
    // confirmed — must not start anything before session creation commits the
    // immutable binding on the selected computer.
    shouldPrewarmDraftACP: (target) => {
      const binding = deps.draftWorkdirBindingFor(target.botId)
      return !binding.id || binding.kind === 'native'
    },
    draftProjectPathFor: (target) => {
      const binding = deps.draftWorkdirBindingFor(target.botId)
      return binding.id && binding.kind === 'native' ? binding.path : ''
    },
  })
  const defaults = createACPDefaults({
    currentBotId: deps.currentBotId,
    sessionId: deps.sessionId,
    explicitSessionSelection: deps.explicitSessionSelection,
    userScopeGeneration: deps.userScopeGeneration,
    currentSelectRequest: deps.currentSelectSessionRequest,
    rememberDefault: staging.rememberDefaultACPInput,
    cachedDefault: staging.cachedDefaultACPInput,
    pendingMatches: orchestration.pendingACPMatchesInput,
    stageDefault: orchestration.stageDefaultACPSession,
  })
  const sessions = createACPSessions({
    currentBotId: deps.currentBotId,
    sessionId: deps.sessionId,
    draftIntent: deps.draftIntent,
    explicitSessionSelection: deps.explicitSessionSelection,
    focusedViewId: deps.focusedViewId,
    userScopeGeneration: deps.userScopeGeneration,
    normalizeTarget: deps.normalizeTarget,
    targetDraftForACP: orchestration.targetDraftForACP,
    pendingACPStateFor: orchestration.pendingACPStateForSession,
    isFocusedTarget: deps.isFocusedTarget,
    upsertSession: deps.upsertSession,
    rememberSession: deps.rememberSession,
    promoteDraftView: deps.promoteDraftView,
    clearRuntimeStatus: runtimeRegistry.clearACPRuntimeStatus,
    forgetDraftStage: orchestration.forgetDraftACPStage,
    discardDraftStage: orchestration.discardDraftACPStage,
    rememberDraftStage: orchestration.rememberDraftACPStage,
    activateDraftStage: orchestration.activateDraftACPStage,
    markSessionDeleted: deps.markSessionDeleted,
    stopSessionRuntime: deps.stopSessionRuntime,
    removeSessionView: deps.removeSessionView,
    removeSessionFromList: deps.removeSessionFromList,
    ensureBot: deps.ensureBot,
    knownSessionSummary: deps.knownSession,
    isDraftCreationActive: target =>
      deps.draftSessionCreations.has(deps.draftCreationKey(target)),
    beginDraftCreation: target => {
      deps.draftSessionCreations.add(deps.draftCreationKey(target))
    },
    endDraftCreation: target => {
      deps.draftSessionCreations.delete(deps.draftCreationKey(target))
    },
    draftWorkdirBindingFor: deps.draftWorkdirBindingFor,
    resolveDraftWorkdirIdFor: deps.resolveDraftWorkdirIdFor,
  })

  const draftViewRequested = ref<{
    botId: string
    viewId: string
    expectedSessionId: string | null
    explicitSelection: boolean
    input: ACPAgentSessionInput | null
    activate: boolean
    seq: number
  } | null>(null)
  let draftViewRequestSeq = 0

  function normalizedInput(input: ACPAgentSessionInput): ACPAgentSessionInput {
    const metadata = acpSessionMetadata(input)
    return {
      ...input,
      agentId: String(metadata.acp_agent_id ?? ''),
      projectPath: String(metadata.project_path ?? ''),
      projectMode: String(metadata.acp_project_mode ?? ''),
    }
  }

  function applyDraftViewRequest(
    request: NonNullable<typeof draftViewRequested.value>,
    mirrorGlobalSelection: boolean,
  ) {
    const target: ChatViewTarget = {
      botId: request.botId,
      sessionId: null,
      viewId: request.viewId,
    }
    if (mirrorGlobalSelection) {
      if (request.input) orchestration.stageNewACPSession(request.input, target)
      else {
        orchestration.resetToEmptyComposer({
          explicitSelection: request.explicitSelection,
          draftIntent: true,
        }, target)
      }
      return
    }
    deps.chatView(target).transcript.clearHistoryView()
    orchestration.discardDraftACPStage(target)
    if (request.input) {
      orchestration.rememberDraftACPStage(target, {
        botId: request.botId,
        input: normalizedInput(request.input),
        runtimeId: '',
      })
    }
  }

  function requestDraftView(
    target: ChatViewTarget,
    input: ACPAgentSessionInput | null,
    activate = deps.isFocusedTarget(target),
  ) {
    const resolved = deps.normalizeTarget(target)
    draftViewRequested.value = {
      botId: resolved.botId,
      viewId: resolved.viewId,
      expectedSessionId: resolved.sessionId,
      explicitSelection: true,
      input: input ? normalizedInput(input) : null,
      activate,
      seq: ++draftViewRequestSeq,
    }
  }

  function reset() {
    staging.clearPendingACPSession()
    staging.clearDefaultACPInputs()
    orchestration.reset()
    runtimeRegistry.resetACPRuntimeRegistry()
    draftViewRequested.value = null
    draftViewCommandVersions.clear()
  }

  return {
    runtimeRegistry,
    staging,
    orchestration,
    defaults,
    sessions,
    draftViewRequested,
    applyDraftViewRequest,
    requestDraftView,
    invalidateDraftViewCommand,
    beginDraftViewCommand,
    reset,
  }
}
