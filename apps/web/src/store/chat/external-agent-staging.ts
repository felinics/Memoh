import { computed, ref, type Ref } from 'vue'
import type { AcpagentRuntimeStatus } from '@memohai/sdk'
import {
  createACPRuntime as requestCreateACPRuntime,
  fetchACPRuntimeByID as requestFetchACPRuntimeByID,
  closeACPRuntime as requestCloseACPRuntime,
  setACPRuntimeModelByID as requestSetACPRuntimeModelByID,
  setACPRuntimeModeByID as requestSetACPRuntimeModeByID,
  setACPRuntimeReasoningByID as requestSetACPRuntimeReasoningByID,
} from '@/composables/api/useChat'
import { ACP_DEFAULT_PROJECT_MODE, ACP_DEFAULT_PROJECT_PATH } from '@/utils/acp'
import { isApiErrorCode } from '@/utils/api-error'
import type { ACPRuntimeStatusRegistry } from './acp-runtime-registry'
import type { ExternalAgentSessionInput } from './types'

// Pending External Agent session staging — the state machine behind the draft
// composer flow: selecting an Agent before any session exists,
// warming a runtime for it, switching its model or reasoning effort, and handing the warm runtime
// over to the real session on first send.
//
// This factory calls transports directly (createACPRuntime / closeACPRuntime /
// setACPRuntimeModelByID) — an exception to the "factories don't touch
// transports" rule, acceptable because tests mock the API module by path and
// this file imports the exact same module. Everything that mutates the
// session list or transcript is injected as a callback instead.

interface PendingExternalAgentStageSnapshot {
  botId: string
  generation: number
  identityKey: string
  runtimeId: string
}

export interface DetachedExternalAgentSession {
  input: ExternalAgentSessionInput
  runtimeId: string
  botId: string
}

export function externalAgentDraftMetadata(input: ExternalAgentSessionInput): Record<string, unknown> {
  const agentId = input.agentId.trim()
  const projectMode = input.projectMode?.trim() || ACP_DEFAULT_PROJECT_MODE
  const projectPath = input.projectPath?.trim() || ACP_DEFAULT_PROJECT_PATH
  return {
    acp_agent_id: agentId,
    project_path: projectPath,
    acp_project_mode: projectMode,
  }
}

export interface ExternalAgentStagingDeps {
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

export function createExternalAgentStaging(deps: ExternalAgentStagingDeps) {
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

  const pendingExternalAgentSessionInput = ref<ExternalAgentSessionInput | null>(null)
  const defaultExternalAgentInputsByBot = new Map<string, ExternalAgentSessionInput | null>()
  // Server-generated ID of the staged runtime; the client never invents
  // runtime identifiers.
  const pendingACPRuntimeId = ref('')
  const pendingExternalAgentBotId = ref('')
  const pendingACPCreating = ref(false)
  let pendingACPCreateRequest: Promise<AcpagentRuntimeStatus | undefined> | null = null
  let pendingACPCreateKey = ''
  let pendingExternalAgentGeneration = 0
  let pendingACPConfigRequestVersion = 0

  const pendingExternalAgentSessionMetadata = computed<Record<string, unknown> | null>(() =>
    pendingExternalAgentSessionInput.value ? externalAgentDraftMetadata(pendingExternalAgentSessionInput.value) : null,
  )
  const pendingACPRuntimeStatus = computed(() => {
    const bid = pendingExternalAgentBotId.value
    const rid = pendingACPRuntimeId.value
    const key = acpRuntimeKey(bid, rid)
    return key ? acpRuntimeStatuses.value[key] : undefined
  })
  const pendingACPRuntimeEnsuring = computed(() => pendingACPCreating.value)

  function cloneExternalAgentInput(input: ExternalAgentSessionInput): ExternalAgentSessionInput {
    return { ...input }
  }

  function rememberDefaultExternalAgentInput(botId: string, input: ExternalAgentSessionInput | null) {
    const bid = botId.trim()
    if (!bid) return
    defaultExternalAgentInputsByBot.set(bid, input ? cloneExternalAgentInput(input) : null)
  }

  function cachedDefaultExternalAgentInput(botId: string): { loaded: boolean, input: ExternalAgentSessionInput | null } {
    const bid = botId.trim()
    if (!defaultExternalAgentInputsByBot.has(bid)) return { loaded: false, input: null }
    const input = defaultExternalAgentInputsByBot.get(bid) ?? null
    return { loaded: true, input: input ? cloneExternalAgentInput(input) : null }
  }

  function clearDefaultExternalAgentInputs() {
    defaultExternalAgentInputsByBot.clear()
  }

  function cacheDefaultExternalAgentSession(input: ExternalAgentSessionInput | null) {
    rememberDefaultExternalAgentInput(currentBotId.value ?? '', input)
  }

  function pendingExternalAgentIdentityKey(botId: string, input: ExternalAgentSessionInput): string {
    return [botId, input.sessionMode ?? 'chat', input.botAgentId ?? '', input.runtime ?? 'acp', input.agentId, input.projectPath ?? '', input.projectMode ?? ''].join('\u0000')
  }

  function pendingExternalAgentStagingKey(snapshot: Pick<PendingExternalAgentStageSnapshot, 'identityKey' | 'generation'>): string {
    return `${snapshot.generation}\u0000${snapshot.identityKey}`
  }

  function nextPendingExternalAgentGeneration() {
    pendingExternalAgentGeneration += 1
  }

  function clearPendingExternalAgentCreateTracking() {
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

  function capturePendingExternalAgentStage(): PendingExternalAgentStageSnapshot | null {
    const botId = pendingExternalAgentBotId.value
    const pending = pendingExternalAgentSessionInput.value
    if (!botId || !pending) return null
    return {
      botId,
      generation: pendingExternalAgentGeneration,
      identityKey: pendingExternalAgentIdentityKey(botId, pending),
      runtimeId: pendingACPRuntimeId.value,
    }
  }

  function isPendingExternalAgentStageCurrent(snapshot: PendingExternalAgentStageSnapshot, configRequestVersion?: number): boolean {
    const current = capturePendingExternalAgentStage()
    if (!current) return false
    return current.botId === snapshot.botId
      && current.generation === snapshot.generation
      && current.identityKey === snapshot.identityKey
      && (configRequestVersion === undefined || pendingACPConfigRequestVersion === configRequestVersion)
  }

  function stageExternalAgentSession(input: ExternalAgentSessionInput, options: { explicitSelection?: boolean } = {}) {
    const ownerBotId = (currentBotId.value ?? '').trim()
    const metadata = externalAgentDraftMetadata(input)
    const existing = pendingExternalAgentSessionInput.value
    const samePendingAgent = Boolean(existing
      && pendingExternalAgentBotId.value === ownerBotId
      && (existing.botAgentId ?? '') === (input.botAgentId?.trim() ?? '')
      && (existing.runtime || 'acp') === (input.runtime || 'acp')
      && existing.agentId === metadata.acp_agent_id
      && (existing.sessionMode || 'chat') === (input.sessionMode || 'chat')
      && (existing.projectPath || ACP_DEFAULT_PROJECT_PATH) === metadata.project_path
      && (existing.projectMode || ACP_DEFAULT_PROJECT_MODE) === metadata.acp_project_mode)
    if (!samePendingAgent) {
      nextPendingExternalAgentGeneration()
      pendingACPConfigRequestVersion += 1
      clearPendingExternalAgentCreateTracking()
    }
    const previousOwnerBotId = pendingExternalAgentBotId.value
    pendingExternalAgentBotId.value = ownerBotId
    pendingExternalAgentSessionInput.value = {
      ...input,
      botAgentId: input.botAgentId?.trim() || undefined,
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

  function stageDefaultExternalAgentSession(input: ExternalAgentSessionInput) {
    rememberDefaultExternalAgentInput(currentBotId.value ?? '', input)
    bumpSelectSessionRequest()
    explicitSessionSelection.value = false
    draftIntent.value = false
    // Stop the live session runtime before clearing sessionId — clearTranscriptForDraft
    // reads the current id to unsubscribe.
    clearTranscriptForDraft()
    sessionId.value = null
    stageExternalAgentSession(input, { explicitSelection: false })
  }

  function stageNewExternalAgentSession(input: ExternalAgentSessionInput) {
    bumpSelectSessionRequest()
    clearPendingExternalAgentSession()
    draftIntent.value = true
    clearTranscriptForDraft()
    sessionId.value = null
    stageExternalAgentSession(input, { explicitSelection: true })
  }

  function resetToEmptyComposer(options: { clearPendingExternalAgent?: boolean; explicitSelection?: boolean; draftIntent?: boolean } = {}) {
    bumpSelectSessionRequest()
    if (options.clearPendingExternalAgent !== false) {
      clearPendingExternalAgentSession()
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
    const snapshot = capturePendingExternalAgentStage()
    const pending = pendingExternalAgentSessionInput.value
    if (!snapshot || !pending) return undefined
    if (pending.runtime && pending.runtime !== 'acp') return undefined
    if (snapshot.runtimeId) {
      try {
        const runtime = await requestFetchACPRuntimeByID(snapshot.botId, snapshot.runtimeId)
        if (!isPendingExternalAgentStageCurrent(snapshot) || pendingACPRuntimeId.value !== snapshot.runtimeId) return undefined
        setACPRuntimeStatus(snapshot.botId, snapshot.runtimeId, runtime)
        return runtime
      } catch (error) {
        if (!isPendingExternalAgentStageCurrent(snapshot)) return undefined
        if (!isRuntimeNotFoundError(error)) throw error
        clearACPRuntimeStatus(snapshot.botId, snapshot.runtimeId)
        if (pendingACPRuntimeId.value === snapshot.runtimeId) pendingACPRuntimeId.value = ''
      }
    }
    const stagingKey = pendingExternalAgentStagingKey(snapshot)
    if (pendingACPCreateRequest && pendingACPCreateKey === stagingKey) return pendingACPCreateRequest

    pendingACPCreating.value = true
    const request = requestCreateACPRuntime(snapshot.botId, {
      agentId: pending.agentId,
      projectPath: pending.projectPath,
    })
      .then((runtime) => {
        const rid = runtime?.runtime_id?.trim() ?? ''
        const current = capturePendingExternalAgentStage()
        const stillStaged = !!current
          && pendingExternalAgentStagingKey(current) === stagingKey
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
        if (!isPendingExternalAgentStageCurrent(snapshot)) return undefined
        throw error
      })
      .finally(() => {
        if (pendingACPCreateRequest === request) {
          clearPendingExternalAgentCreateTracking()
        }
      })
    pendingACPCreateRequest = request
    pendingACPCreateKey = stagingKey
    return request
  }

  async function setPendingACPModel(modelId: string): Promise<AcpagentRuntimeStatus | undefined> {
    if (!pendingExternalAgentSessionInput.value) return
    const mid = modelId.trim()
    if (!mid) throw new Error('ACP model is not selected')

    return setPendingACPConfig((botId, runtimeId) => requestSetACPRuntimeModelByID(botId, runtimeId, mid))
  }

  async function setPendingACPReasoning(effort: string): Promise<AcpagentRuntimeStatus | undefined> {
    if (!pendingExternalAgentSessionInput.value) return
    const value = effort.trim()
    if (!value) throw new Error('ACP reasoning effort is not selected')

    return setPendingACPConfig((botId, runtimeId) => requestSetACPRuntimeReasoningByID(botId, runtimeId, value))
  }

  async function setPendingACPMode(modeId: string): Promise<AcpagentRuntimeStatus | undefined> {
    if (!pendingExternalAgentSessionInput.value) return
    if (!modeId.trim()) throw new Error('ACP mode is not selected')

    return setPendingACPConfig((botId, runtimeId) => requestSetACPRuntimeModeByID(botId, runtimeId, modeId))
  }

  async function setPendingACPConfig(
    update: (botId: string, runtimeId: string) => Promise<AcpagentRuntimeStatus>,
  ): Promise<AcpagentRuntimeStatus | undefined> {
    const initialSnapshot = capturePendingExternalAgentStage()
    if (!initialSnapshot) return
    const requestVersion = ++pendingACPConfigRequestVersion

    const runtimeId = await pendingACPConfigRuntime(initialSnapshot, requestVersion)
    if (!runtimeId) return undefined
    return setPendingACPConfigOnRuntime(initialSnapshot, runtimeId, requestVersion, update)
  }

  async function pendingACPConfigRuntime(snapshot: PendingExternalAgentStageSnapshot, requestVersion: number): Promise<string> {
    const current = capturePendingExternalAgentStage()
    if (!current || !isPendingExternalAgentStageCurrent(snapshot, requestVersion)) return ''
    if (current.runtimeId) return current.runtimeId
    await ensurePendingACPRuntime()
    if (!isPendingExternalAgentStageCurrent(snapshot, requestVersion)) return ''
    return capturePendingExternalAgentStage()?.runtimeId ?? ''
  }

  async function setPendingACPConfigOnRuntime(
    snapshot: PendingExternalAgentStageSnapshot,
    runtimeId: string,
    requestVersion: number,
    update: (botId: string, runtimeId: string) => Promise<AcpagentRuntimeStatus>,
  ): Promise<AcpagentRuntimeStatus | undefined> {
    try {
      const runtime = await update(snapshot.botId, runtimeId)
      if (!isPendingExternalAgentStageCurrent(snapshot, requestVersion)) return undefined
      setACPRuntimeStatus(snapshot.botId, runtimeId, runtime)
      return runtime
    } catch (error) {
      if (!isPendingExternalAgentStageCurrent(snapshot, requestVersion)) return undefined
      if (!isRuntimeNotFoundError(error)) throw error
      if (pendingACPRuntimeId.value !== runtimeId) return undefined

      clearACPRuntimeStatus(snapshot.botId, runtimeId)
      pendingACPRuntimeId.value = ''

      const freshId = await pendingACPConfigRuntime(snapshot, requestVersion)
      if (!freshId) return undefined
      const runtime = await update(snapshot.botId, freshId)
      if (!isPendingExternalAgentStageCurrent(snapshot, requestVersion)) return undefined
      setACPRuntimeStatus(snapshot.botId, freshId, runtime)
      return runtime
    }
  }

  function isRuntimeNotFoundError(error: unknown): boolean {
    return isApiErrorCode(error, 'acp.runtime_not_found')
  }

  function clearPendingExternalAgentSession() {
    const bid = pendingExternalAgentBotId.value
    const runtimeId = pendingACPRuntimeId.value
    nextPendingExternalAgentGeneration()
    pendingACPConfigRequestVersion += 1
    clearPendingExternalAgentCreateTracking()
    closeStagedRuntime(bid, runtimeId)
    pendingExternalAgentSessionInput.value = null
    pendingACPRuntimeId.value = ''
    pendingExternalAgentBotId.value = ''
  }

  // Detaches the staged ACP session without closing its warm runtime, so the
  // first send can bind the runtime to the real session.
  function detachPendingExternalAgentSession(): DetachedExternalAgentSession | null {
    const pending = pendingExternalAgentSessionInput.value
    if (!pending) return null
    const runtimeId = pendingACPRuntimeId.value
    const botId = pendingExternalAgentBotId.value
    nextPendingExternalAgentGeneration()
    pendingACPConfigRequestVersion += 1
    clearPendingExternalAgentCreateTracking()
    pendingExternalAgentSessionInput.value = null
    pendingACPRuntimeId.value = ''
    pendingExternalAgentBotId.value = ''
    return { input: { ...pending }, runtimeId, botId }
  }

  function restorePendingExternalAgentSession(input: ExternalAgentSessionInput, runtimeId: string, botId: string) {
    pendingExternalAgentSessionInput.value = { ...input }
    pendingACPRuntimeId.value = runtimeId.trim()
    pendingExternalAgentBotId.value = botId.trim()
  }

  function releasePendingExternalAgentSession() {
    nextPendingExternalAgentGeneration()
    pendingACPConfigRequestVersion += 1
    clearPendingExternalAgentCreateTracking()
    pendingExternalAgentSessionInput.value = null
    pendingACPRuntimeId.value = ''
    pendingExternalAgentBotId.value = ''
  }

  function discardDetachedExternalAgentSession(detached: DetachedExternalAgentSession) {
    closeStagedRuntime(detached.botId, detached.runtimeId)
  }

  function pendingExternalAgentMatchesInput(input: ExternalAgentSessionInput): boolean {
    const pending = pendingExternalAgentSessionInput.value
    if (!pending || sessionId.value) return false
    const metadata = externalAgentDraftMetadata(input)
    return pending.agentId === metadata.acp_agent_id
      && (pending.botAgentId ?? '') === (input.botAgentId?.trim() ?? '')
      && (pending.runtime || 'acp') === (input.runtime || 'acp')
      && (pending.sessionMode || 'chat') === (input.sessionMode || 'chat')
      && (pending.projectPath || ACP_DEFAULT_PROJECT_PATH) === metadata.project_path
      && (pending.projectMode || ACP_DEFAULT_PROJECT_MODE) === metadata.acp_project_mode
  }

  return {
    pendingExternalAgentSessionInput,
    pendingACPRuntimeId,
    pendingExternalAgentSessionMetadata,
    pendingACPRuntimeStatus,
    pendingACPRuntimeEnsuring,
    rememberDefaultExternalAgentInput,
    cachedDefaultExternalAgentInput,
    clearDefaultExternalAgentInputs,
    cacheDefaultExternalAgentSession,
    stageExternalAgentSession,
    stageDefaultExternalAgentSession,
    stageNewExternalAgentSession,
    resetToEmptyComposer,
    ensurePendingACPRuntime,
    setPendingACPModel,
    setPendingACPMode,
    setPendingACPReasoning,
    clearPendingExternalAgentSession,
    detachPendingExternalAgentSession,
    restorePendingExternalAgentSession,
    releasePendingExternalAgentSession,
    discardDetachedExternalAgentSession,
    pendingExternalAgentMatchesInput,
  }
}
