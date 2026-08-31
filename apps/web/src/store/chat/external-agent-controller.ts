import { ref, type Ref } from 'vue'
import { externalAgentDraftMetadata, createExternalAgentStaging } from './external-agent-staging'
import { createExternalAgentOrchestration } from './external-agent-orchestration'
import { createACPRuntimeRegistry } from './acp-runtime-registry'
import { createExternalAgentSessions } from './external-agent-sessions'
import { createExternalAgentDefaults } from './external-agent-defaults'
import type { ChatViewEntry } from './view-registry'
import type {
  ExternalAgentSessionInput,
  ChatViewTarget,
} from './types'
import type { SessionSummary } from '@/composables/api/useChat'

export function createExternalAgentController(deps: {
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
  draftWorkdirIdFor: (botId: string, opts: { externalAgent: boolean }) => string
}) {
  const runtimeRegistry = createACPRuntimeRegistry({
    currentBotId: deps.currentBotId,
    sessionId: deps.sessionId,
  })
  const staging = createExternalAgentStaging({
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

  const orchestration = createExternalAgentOrchestration({
    staging,
    runtimeRegistry,
    normalizeTarget: deps.normalizeTarget,
    invalidateDraftCommand: invalidateDraftViewCommand,
    forgetDraftCommand: target => {
      draftViewCommandVersions.delete(deps.draftCreationKey(target))
    },
    resetWorkspaceTargetSelection: deps.resetWorkspaceTargetSelection,
  })
  const defaults = createExternalAgentDefaults({
    currentBotId: deps.currentBotId,
    sessionId: deps.sessionId,
    explicitSessionSelection: deps.explicitSessionSelection,
    userScopeGeneration: deps.userScopeGeneration,
    currentSelectRequest: deps.currentSelectSessionRequest,
    rememberDefault: staging.rememberDefaultExternalAgentInput,
    cachedDefault: staging.cachedDefaultExternalAgentInput,
    pendingMatches: orchestration.pendingExternalAgentMatchesInput,
    stageDefault: orchestration.stageDefaultExternalAgentSession,
  })
  const sessions = createExternalAgentSessions({
    currentBotId: deps.currentBotId,
    sessionId: deps.sessionId,
    draftIntent: deps.draftIntent,
    explicitSessionSelection: deps.explicitSessionSelection,
    focusedViewId: deps.focusedViewId,
    userScopeGeneration: deps.userScopeGeneration,
    normalizeTarget: deps.normalizeTarget,
    targetDraftForExternalAgent: orchestration.targetDraftForExternalAgent,
    pendingExternalAgentStateFor: orchestration.pendingExternalAgentStateFor,
    isFocusedTarget: deps.isFocusedTarget,
    upsertSession: deps.upsertSession,
    rememberSession: deps.rememberSession,
    promoteDraftView: deps.promoteDraftView,
    clearRuntimeStatus: runtimeRegistry.clearACPRuntimeStatus,
    forgetDraftStage: orchestration.forgetDraftExternalAgentStage,
    discardDraftStage: orchestration.discardDraftExternalAgentStage,
    rememberDraftStage: orchestration.rememberDraftExternalAgentStage,
    activateDraftStage: orchestration.activateDraftExternalAgentStage,
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
    draftWorkdirIdFor: deps.draftWorkdirIdFor,
  })

  const draftViewRequested = ref<{
    botId: string
    viewId: string
    expectedSessionId: string | null
    explicitSelection: boolean
    input: ExternalAgentSessionInput | null
    activate: boolean
    seq: number
  } | null>(null)
  let draftViewRequestSeq = 0

  function normalizedInput(input: ExternalAgentSessionInput): ExternalAgentSessionInput {
    const metadata = externalAgentDraftMetadata(input)
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
      if (request.input) orchestration.stageNewExternalAgentSession(request.input, target)
      else {
        orchestration.resetToEmptyComposer({
          explicitSelection: request.explicitSelection,
          draftIntent: true,
        }, target)
      }
      return
    }
    deps.chatView(target).transcript.clearHistoryView()
    orchestration.discardDraftExternalAgentStage(target)
    if (request.input) {
      orchestration.rememberDraftExternalAgentStage(target, {
        botId: request.botId,
        input: normalizedInput(request.input),
        runtimeId: '',
      })
    }
  }

  function requestDraftView(
    target: ChatViewTarget,
    input: ExternalAgentSessionInput | null,
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
    staging.clearPendingExternalAgentSession()
    staging.clearDefaultExternalAgentInputs()
    orchestration.reset()
    runtimeRegistry.resetExternalAgentRuntimeRegistry()
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
