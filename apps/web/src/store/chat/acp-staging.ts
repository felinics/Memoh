import { computed, ref, type Ref } from 'vue'
import type { AcpagentRuntimeStatus } from '@memohai/sdk'
import {
  createACPRuntime as requestCreateACPRuntime,
  fetchACPRuntimeByID as requestFetchACPRuntimeByID,
  closeACPRuntime as requestCloseACPRuntime,
  setACPRuntimeModelByID as requestSetACPRuntimeModelByID,
  setACPRuntimeReasoningByID as requestSetACPRuntimeReasoningByID,
} from '@/composables/api/useChat'
import { ACP_DEFAULT_PROJECT_MODE, ACP_DEFAULT_PROJECT_PATH } from '@/utils/acp'
import { isApiErrorCode } from '@/utils/api-error'
import type { ACPRuntimeStatusRegistry } from './acp-runtime-registry'
import type { ACPAgentSessionInput } from './types'

// Pending-ACP session staging — the state machine behind the "draft composer
// pointed at an ACP agent" flow: staging an agent before any session exists,
// warming a runtime for it, switching its model or reasoning effort, and handing the warm runtime
// over to the real session on first send.
//
// This factory calls transports directly (createACPRuntime / closeACPRuntime /
// setACPRuntimeModelByID) — an exception to the "factories don't touch
// transports" rule, acceptable because tests mock the API module by path and
// this file imports the exact same module. Everything that mutates the
// session list or transcript is injected as a callback instead.

interface PendingACPStageSnapshot {
  botId: string
  generation: number
  identityKey: string
  runtimeId: string
}

export interface DetachedACPSession {
  input: ACPAgentSessionInput
  runtimeId: string
  botId: string
}

export function acpSessionMetadata(input: ACPAgentSessionInput): Record<string, unknown> {
  const agentId = input.agentId.trim()
  const projectMode = input.projectMode?.trim() || ACP_DEFAULT_PROJECT_MODE
  const projectPath = input.projectPath?.trim() || ACP_DEFAULT_PROJECT_PATH
  return {
    acp_agent_id: agentId,
    project_path: projectPath,
    acp_project_mode: projectMode,
  }
}

export interface ACPStagingDeps {
  currentBotId: Ref<string | null>
  sessionId: Ref<string | null>
  draftIntent: Ref<boolean>
  explicitSessionSelection: Ref<boolean>
  // Shared by staged runtimes and real session runtimes, but owned by one
  // registry so neither flow can mutate its lookup containers directly.
  runtimeRegistry: ACPRuntimeStatusRegistry
  // Invalidates any in-flight selectSession/hydration work in the store.
  bumpSelectSessionRequest: () => void
  // Stops the per-session SSE stream and empties the transcript, resetting
  // pagination to the "no history" draft posture.
  clearTranscriptForDraft: () => void
}

export function createACPStaging(deps: ACPStagingDeps) {
  const {
    currentBotId,
    sessionId,
    draftIntent,
    explicitSessionSelection,
    runtimeRegistry,
    bumpSelectSessionRequest,
    clearTranscriptForDraft,
  } = deps
  const {
    acpRuntimeStatuses,
    acpRuntimeKey,
    setACPRuntimeStatus,
    clearACPRuntimeStatus,
  } = runtimeRegistry

  const pendingACPSessionInput = ref<ACPAgentSessionInput | null>(null)
  const defaultACPInputsByBot = new Map<string, ACPAgentSessionInput | null>()
  // Server-generated ID of the staged runtime; the client never invents
  // runtime identifiers.
  const pendingACPRuntimeId = ref('')
  const pendingACPBotId = ref('')
  const pendingACPCreating = ref(false)
  let pendingACPCreateRequest: Promise<AcpagentRuntimeStatus | undefined> | null = null
  let pendingACPCreateKey = ''
  let pendingACPGeneration = 0
  let pendingACPConfigRequestVersion = 0

  const pendingACPSessionMetadata = computed<Record<string, unknown> | null>(() =>
    pendingACPSessionInput.value ? acpSessionMetadata(pendingACPSessionInput.value) : null,
  )
  const pendingACPRuntimeStatus = computed(() => {
    const bid = pendingACPBotId.value
    const rid = pendingACPRuntimeId.value
    const key = acpRuntimeKey(bid, rid)
    return key ? acpRuntimeStatuses.value[key] : undefined
  })
  const pendingACPRuntimeEnsuring = computed(() => pendingACPCreating.value)

  function cloneACPInput(input: ACPAgentSessionInput): ACPAgentSessionInput {
    return { ...input }
  }

  function rememberDefaultACPInput(botId: string, input: ACPAgentSessionInput | null) {
    const bid = botId.trim()
    if (!bid) return
    defaultACPInputsByBot.set(bid, input ? cloneACPInput(input) : null)
  }

  function cachedDefaultACPInput(botId: string): { loaded: boolean, input: ACPAgentSessionInput | null } {
    const bid = botId.trim()
    if (!defaultACPInputsByBot.has(bid)) return { loaded: false, input: null }
    const input = defaultACPInputsByBot.get(bid) ?? null
    return { loaded: true, input: input ? cloneACPInput(input) : null }
  }

  function cacheDefaultACPSession(input: ACPAgentSessionInput | null) {
    rememberDefaultACPInput(currentBotId.value ?? '', input)
  }

  function pendingACPIdentityKey(botId: string, input: ACPAgentSessionInput): string {
    return [botId, input.sessionMode ?? 'chat', input.agentId, input.projectPath ?? '', input.projectMode ?? ''].join('\u0000')
  }

  function pendingACPStagingKey(snapshot: Pick<PendingACPStageSnapshot, 'identityKey' | 'generation'>): string {
    return `${snapshot.generation}\u0000${snapshot.identityKey}`
  }

  function nextPendingACPGeneration() {
    pendingACPGeneration += 1
  }

  function clearPendingACPCreateTracking() {
    pendingACPCreateRequest = null
    pendingACPCreateKey = ''
    pendingACPCreating.value = false
  }

  function closeStagedRuntime(botId: string, runtimeId: string) {
    const bid = botId.trim()
    const rid = runtimeId.trim()
    if (!bid || !rid) return
    void requestCloseACPRuntime(bid, rid).catch(() => {})
    clearACPRuntimeStatus(bid, rid)
  }

  function capturePendingACPStage(): PendingACPStageSnapshot | null {
    const botId = pendingACPBotId.value
    const pending = pendingACPSessionInput.value
    if (!botId || !pending) return null
    return {
      botId,
      generation: pendingACPGeneration,
      identityKey: pendingACPIdentityKey(botId, pending),
      runtimeId: pendingACPRuntimeId.value,
    }
  }

  function isPendingACPStageCurrent(snapshot: PendingACPStageSnapshot, configRequestVersion?: number): boolean {
    const current = capturePendingACPStage()
    if (!current) return false
    return current.botId === snapshot.botId
      && current.generation === snapshot.generation
      && current.identityKey === snapshot.identityKey
      && (configRequestVersion === undefined || pendingACPConfigRequestVersion === configRequestVersion)
  }

  function stageACPSession(input: ACPAgentSessionInput, options: { explicitSelection?: boolean } = {}) {
    const ownerBotId = (currentBotId.value ?? '').trim()
    const metadata = acpSessionMetadata(input)
    const existing = pendingACPSessionInput.value
    const samePendingAgent = Boolean(existing
      && pendingACPBotId.value === ownerBotId
      && existing.agentId === metadata.acp_agent_id
      && (existing.sessionMode || 'chat') === (input.sessionMode || 'chat')
      && (existing.projectPath || ACP_DEFAULT_PROJECT_PATH) === metadata.project_path
      && (existing.projectMode || ACP_DEFAULT_PROJECT_MODE) === metadata.acp_project_mode)
    if (!samePendingAgent) {
      nextPendingACPGeneration()
      pendingACPConfigRequestVersion += 1
      clearPendingACPCreateTracking()
    }
    const previousOwnerBotId = pendingACPBotId.value
    pendingACPBotId.value = ownerBotId
    pendingACPSessionInput.value = {
      ...input,
      agentId: String(metadata.acp_agent_id ?? ''),
      projectPath: String(metadata.project_path ?? ''),
      projectMode: String(metadata.acp_project_mode ?? ''),
    }
    if (!samePendingAgent && pendingACPRuntimeId.value) {
      const bid = previousOwnerBotId
      const runtimeId = pendingACPRuntimeId.value
      pendingACPRuntimeId.value = ''
      closeStagedRuntime(bid, runtimeId)
    }
    explicitSessionSelection.value = options.explicitSelection !== false
  }

  function stageDefaultACPSession(input: ACPAgentSessionInput) {
    rememberDefaultACPInput(currentBotId.value ?? '', input)
    bumpSelectSessionRequest()
    explicitSessionSelection.value = false
    draftIntent.value = false
    // Stop the live session runtime before clearing sessionId — clearTranscriptForDraft
    // reads the current id to unsubscribe.
    clearTranscriptForDraft()
    sessionId.value = null
    stageACPSession(input, { explicitSelection: false })
  }

  function stageNewACPSession(input: ACPAgentSessionInput) {
    bumpSelectSessionRequest()
    clearPendingACPSession()
    draftIntent.value = true
    clearTranscriptForDraft()
    sessionId.value = null
    stageACPSession(input, { explicitSelection: true })
  }

  function resetToEmptyComposer(options: { clearPendingACP?: boolean; explicitSelection?: boolean; draftIntent?: boolean } = {}) {
    bumpSelectSessionRequest()
    if (options.clearPendingACP !== false) {
      clearPendingACPSession()
    }
    // Must run while sessionId is still set: clearTranscriptForDraft stops the
    // session runtime by reading the current id. Nulling first leaves an orphan
    // subscribe (File/Preview restore clearing initialize()'s auto-pick).
    clearTranscriptForDraft()
    sessionId.value = null
    explicitSessionSelection.value = options.explicitSelection === true
    draftIntent.value = options.draftIntent ?? options.explicitSelection === true
  }

  async function ensurePendingACPRuntime(): Promise<AcpagentRuntimeStatus | undefined> {
    const snapshot = capturePendingACPStage()
    const pending = pendingACPSessionInput.value
    if (!snapshot || !pending) return undefined
    if (snapshot.runtimeId) {
      try {
        const runtime = await requestFetchACPRuntimeByID(snapshot.botId, snapshot.runtimeId)
        if (!isPendingACPStageCurrent(snapshot) || pendingACPRuntimeId.value !== snapshot.runtimeId) return undefined
        setACPRuntimeStatus(snapshot.botId, snapshot.runtimeId, runtime)
        return runtime
      } catch (error) {
        if (!isPendingACPStageCurrent(snapshot)) return undefined
        if (!isRuntimeNotFoundError(error)) throw error
        clearACPRuntimeStatus(snapshot.botId, snapshot.runtimeId)
        if (pendingACPRuntimeId.value === snapshot.runtimeId) pendingACPRuntimeId.value = ''
      }
    }
    const stagingKey = pendingACPStagingKey(snapshot)
    if (pendingACPCreateRequest && pendingACPCreateKey === stagingKey) return pendingACPCreateRequest

    pendingACPCreating.value = true
    const request = requestCreateACPRuntime(snapshot.botId, {
      agentId: pending.agentId,
      projectPath: pending.projectPath,
    })
      .then((runtime) => {
        const rid = runtime?.runtime_id?.trim() ?? ''
        const current = capturePendingACPStage()
        const stillStaged = !!current
          && pendingACPStagingKey(current) === stagingKey
          && !current.runtimeId
        if (stillStaged && rid) {
          pendingACPRuntimeId.value = rid
          setACPRuntimeStatus(snapshot.botId, rid, runtime)
        } else if (rid) {
          // Staging changed while the runtime was starting: discard it.
          closeStagedRuntime(snapshot.botId, rid)
        }
        return runtime
      })
      .catch((error) => {
        if (!isPendingACPStageCurrent(snapshot)) return undefined
        throw error
      })
      .finally(() => {
        if (pendingACPCreateRequest === request) {
          clearPendingACPCreateTracking()
        }
      })
    pendingACPCreateRequest = request
    pendingACPCreateKey = stagingKey
    return request
  }

  async function setPendingACPModel(modelId: string): Promise<AcpagentRuntimeStatus | undefined> {
    if (!pendingACPSessionInput.value) return
    const mid = modelId.trim()
    if (!mid) throw new Error('ACP model is not selected')

    return setPendingACPConfig((botId, runtimeId) => requestSetACPRuntimeModelByID(botId, runtimeId, mid))
  }

  async function setPendingACPReasoning(effort: string): Promise<AcpagentRuntimeStatus | undefined> {
    if (!pendingACPSessionInput.value) return
    const value = effort.trim()
    if (!value) throw new Error('ACP reasoning effort is not selected')

    return setPendingACPConfig((botId, runtimeId) => requestSetACPRuntimeReasoningByID(botId, runtimeId, value))
  }

  async function setPendingACPConfig(
    update: (botId: string, runtimeId: string) => Promise<AcpagentRuntimeStatus>,
  ): Promise<AcpagentRuntimeStatus | undefined> {
    const initialSnapshot = capturePendingACPStage()
    if (!initialSnapshot) return
    const requestVersion = ++pendingACPConfigRequestVersion

    const runtimeId = await pendingACPConfigRuntime(initialSnapshot, requestVersion)
    if (!runtimeId) return undefined
    return setPendingACPConfigOnRuntime(initialSnapshot, runtimeId, requestVersion, update)
  }

  async function pendingACPConfigRuntime(snapshot: PendingACPStageSnapshot, requestVersion: number): Promise<string> {
    const current = capturePendingACPStage()
    if (!current || !isPendingACPStageCurrent(snapshot, requestVersion)) return ''
    if (current.runtimeId) return current.runtimeId
    await ensurePendingACPRuntime()
    if (!isPendingACPStageCurrent(snapshot, requestVersion)) return ''
    return capturePendingACPStage()?.runtimeId ?? ''
  }

  async function setPendingACPConfigOnRuntime(
    snapshot: PendingACPStageSnapshot,
    runtimeId: string,
    requestVersion: number,
    update: (botId: string, runtimeId: string) => Promise<AcpagentRuntimeStatus>,
  ): Promise<AcpagentRuntimeStatus | undefined> {
    try {
      const runtime = await update(snapshot.botId, runtimeId)
      if (!isPendingACPStageCurrent(snapshot, requestVersion)) return undefined
      setACPRuntimeStatus(snapshot.botId, runtimeId, runtime)
      return runtime
    } catch (error) {
      if (!isPendingACPStageCurrent(snapshot, requestVersion)) return undefined
      if (!isRuntimeNotFoundError(error)) throw error
      if (pendingACPRuntimeId.value !== runtimeId) return undefined

      clearACPRuntimeStatus(snapshot.botId, runtimeId)
      pendingACPRuntimeId.value = ''

      const freshId = await pendingACPConfigRuntime(snapshot, requestVersion)
      if (!freshId) return undefined
      const runtime = await update(snapshot.botId, freshId)
      if (!isPendingACPStageCurrent(snapshot, requestVersion)) return undefined
      setACPRuntimeStatus(snapshot.botId, freshId, runtime)
      return runtime
    }
  }

  function isRuntimeNotFoundError(error: unknown): boolean {
    return isApiErrorCode(error, 'acp.runtime_not_found')
  }

  function clearPendingACPSession() {
    const bid = pendingACPBotId.value
    const runtimeId = pendingACPRuntimeId.value
    nextPendingACPGeneration()
    pendingACPConfigRequestVersion += 1
    clearPendingACPCreateTracking()
    closeStagedRuntime(bid, runtimeId)
    pendingACPSessionInput.value = null
    pendingACPRuntimeId.value = ''
    pendingACPBotId.value = ''
  }

  // Detaches the staged ACP session without closing its warm runtime, so the
  // first send can bind the runtime to the real session.
  function detachPendingACPSession(): DetachedACPSession | null {
    const pending = pendingACPSessionInput.value
    if (!pending) return null
    const runtimeId = pendingACPRuntimeId.value
    const botId = pendingACPBotId.value
    nextPendingACPGeneration()
    pendingACPConfigRequestVersion += 1
    clearPendingACPCreateTracking()
    pendingACPSessionInput.value = null
    pendingACPRuntimeId.value = ''
    pendingACPBotId.value = ''
    return { input: { ...pending }, runtimeId, botId }
  }

  function restorePendingACPSession(input: ACPAgentSessionInput, runtimeId: string, botId: string) {
    pendingACPSessionInput.value = { ...input }
    pendingACPRuntimeId.value = runtimeId.trim()
    pendingACPBotId.value = botId.trim()
  }

  function releasePendingACPSession() {
    nextPendingACPGeneration()
    pendingACPConfigRequestVersion += 1
    clearPendingACPCreateTracking()
    pendingACPSessionInput.value = null
    pendingACPRuntimeId.value = ''
    pendingACPBotId.value = ''
  }

  function discardDetachedACPSession(detached: DetachedACPSession) {
    closeStagedRuntime(detached.botId, detached.runtimeId)
  }

  function pendingACPMatchesInput(input: ACPAgentSessionInput): boolean {
    const pending = pendingACPSessionInput.value
    if (!pending || sessionId.value) return false
    const metadata = acpSessionMetadata(input)
    return pending.agentId === metadata.acp_agent_id
      && (pending.sessionMode || 'chat') === (input.sessionMode || 'chat')
      && (pending.projectPath || ACP_DEFAULT_PROJECT_PATH) === metadata.project_path
      && (pending.projectMode || ACP_DEFAULT_PROJECT_MODE) === metadata.acp_project_mode
  }

  return {
    pendingACPSessionInput,
    pendingACPRuntimeId,
    pendingACPSessionMetadata,
    pendingACPRuntimeStatus,
    pendingACPRuntimeEnsuring,
    rememberDefaultACPInput,
    cachedDefaultACPInput,
    cacheDefaultACPSession,
    stageACPSession,
    stageDefaultACPSession,
    stageNewACPSession,
    resetToEmptyComposer,
    ensurePendingACPRuntime,
    setPendingACPModel,
    setPendingACPReasoning,
    clearPendingACPSession,
    detachPendingACPSession,
    restorePendingACPSession,
    releasePendingACPSession,
    discardDetachedACPSession,
    pendingACPMatchesInput,
  }
}
