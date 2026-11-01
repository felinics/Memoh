import { EXTERNAL_AGENT_DEFAULT_PROJECT_PATH } from '@/utils/external-agent'
import { computed, ref, type Ref } from 'vue'
import type { AcpagentRuntimeStatus } from '@memohai/sdk'
import { fetchACPAgentOptions as requestACPAgentOptions } from '@/composables/api/useChat'
import { botAgentRuntimeForProvider, normalizeBotAgentRuntime, type BotAgentRuntime } from '@/utils/bot-agent'
import type { ExternalAgentSessionInput } from './types'

// Pending External Agent session staging — the state machine behind the draft
// composer flow: selecting an Agent before any session exists, reading what
// it offers, and keeping the model, reasoning effort and mode picked for it.
// The picks travel with the session's creation; no agent process waits for
// them.
//
// This factory calls the options transport directly — an exception to the
// "factories don't touch transports" rule, acceptable because tests mock the
// API module by path and this file imports the exact same module. Everything
// that mutates the session list or transcript is injected as a callback.

interface PendingExternalAgentStageSnapshot {
  botId: string
  generation: number
  identityKey: string
}

export interface DetachedExternalAgentSession {
  input: ExternalAgentSessionInput
  botId: string
  // The Agent's options, once read, stay with the draft: another view taking
  // the composer must not leave this one without them.
  options?: AcpagentRuntimeStatus
}

// draftRuntimeStatus is the offered options with the picks made so far shown
// as current.
export function draftRuntimeStatus(options: AcpagentRuntimeStatus | undefined, pending: ExternalAgentSessionInput | null | undefined): AcpagentRuntimeStatus | undefined {
  if (!options || !pending) return options
  return {
    ...options,
    models: options.models && pending.acpModelId ? { ...options.models, current_model_id: pending.acpModelId } : options.models,
    reasoning: options.reasoning && pending.acpReasoningEffort ? { ...options.reasoning, current_effort: pending.acpReasoningEffort } : options.reasoning,
    modes: options.modes && pending.permissionMode ? { ...options.modes, current_mode_id: pending.permissionMode } : options.modes,
  }
}

// Normalize legacy input objects once before staging or session creation.
// Internal draft identity must not infer a different runtime from the writer.
export function normalizedExternalAgentInput(input: ExternalAgentSessionInput): ExternalAgentSessionInput & { runtime: BotAgentRuntime } {
  return {
    ...input,
    runtime: normalizeBotAgentRuntime(input.runtime) || botAgentRuntimeForProvider(input.agentId),
    botAgentId: input.botAgentId?.trim() || undefined,
    agentId: input.agentId.trim(),
    projectPath: input.projectPath?.trim() || EXTERNAL_AGENT_DEFAULT_PROJECT_PATH,
  }
}

// What a staged draft's session will store: its project. The Agent instance
// carries the ACP profile.
export function externalAgentDraftMetadata(input: ExternalAgentSessionInput): Record<string, unknown> {
  return { project_path: input.projectPath?.trim() || EXTERNAL_AGENT_DEFAULT_PROJECT_PATH }
}

// Identity of a pending External Agent draft. Every "is this the same staged
// draft?" decision — keep-or-replace the warm runtime, skip re-staging the
// bot default, pane-level matching — must agree on the field set, so they all
// route through here. sessionMode is part of the identity: a chat draft and a
// discuss draft for the same agent are different sessions.
export function sameExternalAgentSessionInput(a: ExternalAgentSessionInput, b: ExternalAgentSessionInput): boolean {
  const left = normalizedExternalAgentInput(a)
  const right = normalizedExternalAgentInput(b)
  return left.agentId === right.agentId
    && (left.botAgentId ?? '') === (right.botAgentId ?? '')
    && left.runtime === right.runtime
    && (a.sessionMode || 'chat') === (b.sessionMode || 'chat')
    && left.projectPath === right.projectPath
}

export interface ExternalAgentStagingDeps {
  currentBotId: Ref<string | null>
  sessionId: Ref<string | null>
  draftIntent: Ref<boolean>
  explicitSessionSelection: Ref<boolean>
  // Invalidates any in-flight selectSession/hydration work in the store.
  bumpSelectSessionRequest: () => void
  // Stops the per-session SSE stream and empties the transcript, resetting
  // pagination to the "no history" draft posture.
  clearTranscriptForDraft: () => void
  // The path of the folder a new External Agent session of the bot would
  // bind, '' when none: the server makes it the session's project.
  draftFolderPathFor: (botId: string) => string
}

export function createExternalAgentStaging(deps: ExternalAgentStagingDeps) {
  const {
    currentBotId,
    sessionId,
    draftIntent,
    explicitSessionSelection,
    bumpSelectSessionRequest,
    clearTranscriptForDraft,
    draftFolderPathFor,
  } = deps

  const pendingExternalAgentSessionInput = ref<ExternalAgentSessionInput | null>(null)
  const defaultExternalAgentInputsByBot = new Map<string, ExternalAgentSessionInput | null>()
  const pendingExternalAgentBotId = ref('')
  // What the staged agent offers, as its own process reported it.
  const pendingACPOptions = ref<AcpagentRuntimeStatus | undefined>()
  const pendingACPLoading = ref(false)
  let pendingACPOptionsRequest: Promise<AcpagentRuntimeStatus | undefined> | null = null
  let pendingACPOptionsKey = ''
  let pendingExternalAgentGeneration = 0

  // A draft as its session will be created: in the folder it will bind, when
  // there is one. Everything shown or read for a draft (its project, the
  // options read there) goes through here; the draft keeps its own project
  // for when the folder is cleared.
  function effectiveDraftInput(botId: string, input: ExternalAgentSessionInput): ExternalAgentSessionInput {
    const folder = draftFolderPathFor(botId).trim()
    return folder ? { ...input, projectPath: folder } : input
  }
  const effectivePendingInput = computed<ExternalAgentSessionInput | null>(() => {
    const pending = pendingExternalAgentSessionInput.value
    return pending ? effectiveDraftInput(pendingExternalAgentBotId.value, pending) : null
  })
  const currentPendingACPOptions = computed(() => {
    const options = pendingACPOptions.value
    return options && options.project_path === draftProject(effectivePendingInput.value) ? options : undefined
  })

  function draftProject(input: ExternalAgentSessionInput | null): string {
    return input?.projectPath?.trim() || EXTERNAL_AGENT_DEFAULT_PROJECT_PATH
  }

  // readACPOptions reads what input's agent offers in input's project, marked
  // with that project so they are never taken for another's.
  async function readACPOptions(botId: string, input: ExternalAgentSessionInput, modelId?: string): Promise<AcpagentRuntimeStatus> {
    const projectPath = draftProject(input)
    const options = await requestACPAgentOptions(botId, { agentId: input.agentId, botAgentId: input.botAgentId, projectPath, modelId })
    return { ...options, project_path: projectPath }
  }
  const pendingExternalAgentSessionMetadata = computed<Record<string, unknown> | null>(() =>
    effectivePendingInput.value ? externalAgentDraftMetadata(effectivePendingInput.value) : null,
  )
  const pendingACPRuntimeStatus = computed(() => draftRuntimeStatus(currentPendingACPOptions.value, effectivePendingInput.value))
  const pendingACPRuntimeEnsuring = computed(() => pendingACPLoading.value)

  function cloneExternalAgentInput(input: ExternalAgentSessionInput): ExternalAgentSessionInput {
    return normalizedExternalAgentInput(input)
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
    input = normalizedExternalAgentInput(input)
    return [botId, input.sessionMode ?? 'chat', input.botAgentId ?? '', input.runtime ?? 'acp', input.agentId, input.projectPath ?? ''].join('\u0000')
  }

  function pendingExternalAgentStagingKey(snapshot: Pick<PendingExternalAgentStageSnapshot, 'identityKey' | 'generation'>): string {
    return `${snapshot.generation}\u0000${snapshot.identityKey}`
  }

  function nextPendingExternalAgentGeneration() {
    pendingExternalAgentGeneration += 1
  }

  function clearPendingACPOptions() {
    pendingACPOptionsRequest = null
    pendingACPOptionsKey = ''
    pendingACPLoading.value = false
    pendingACPOptions.value = undefined
  }

  function capturePendingExternalAgentStage(): PendingExternalAgentStageSnapshot | null {
    const botId = pendingExternalAgentBotId.value
    const pending = effectivePendingInput.value
    if (!botId || !pending) return null
    return {
      botId,
      generation: pendingExternalAgentGeneration,
      identityKey: pendingExternalAgentIdentityKey(botId, pending),
    }
  }

  function isPendingExternalAgentStageCurrent(snapshot: PendingExternalAgentStageSnapshot): boolean {
    const current = capturePendingExternalAgentStage()
    return !!current
      && current.botId === snapshot.botId
      && current.generation === snapshot.generation
      && current.identityKey === snapshot.identityKey
  }

  function stageExternalAgentSession(input: ExternalAgentSessionInput, options: { explicitSelection?: boolean } = {}) {
    const ownerBotId = (currentBotId.value ?? '').trim()
    input = normalizedExternalAgentInput(input)
    const existing = pendingExternalAgentSessionInput.value
    const samePendingAgent = Boolean(existing
      && pendingExternalAgentBotId.value === ownerBotId
      && sameExternalAgentSessionInput(existing, input))
    if (samePendingAgent && existing) {
      // Re-staging the same draft keeps what was picked for it.
      input = { ...input, acpModelId: input.acpModelId ?? existing.acpModelId, acpReasoningEffort: input.acpReasoningEffort ?? existing.acpReasoningEffort }
    } else {
      nextPendingExternalAgentGeneration()
      clearPendingACPOptions()
    }
    pendingExternalAgentBotId.value = ownerBotId
    pendingExternalAgentSessionInput.value = input
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
    const pending = effectivePendingInput.value
    if (!snapshot || !pending) return undefined
    if (pending.runtime && pending.runtime !== 'acp') return undefined
    if (currentPendingACPOptions.value) return pendingACPRuntimeStatus.value
    const stagingKey = pendingExternalAgentStagingKey(snapshot)
    if (pendingACPOptionsRequest && pendingACPOptionsKey === stagingKey) return pendingACPOptionsRequest

    pendingACPLoading.value = true
    const request = readACPOptions(snapshot.botId, pending)
      .then((options) => {
        if (!isPendingExternalAgentStageCurrent(snapshot)) return undefined
        pendingACPOptions.value = options
        return pendingACPRuntimeStatus.value
      })
      .catch((error) => {
        if (!isPendingExternalAgentStageCurrent(snapshot)) return undefined
        throw error
      })
      .finally(() => {
        if (pendingACPOptionsRequest === request) {
          pendingACPOptionsRequest = null
          pendingACPOptionsKey = ''
          pendingACPLoading.value = false
        }
      })
    pendingACPOptionsRequest = request
    pendingACPOptionsKey = stagingKey
    return request
  }

  // pickPendingACP records a pick for the staged draft once the agent's
  // options are known; the pick is checked against them.
  async function pickPendingACP(
    offered: (options: AcpagentRuntimeStatus) => boolean,
    pick: (input: ExternalAgentSessionInput) => ExternalAgentSessionInput,
  ): Promise<AcpagentRuntimeStatus | undefined> {
    const snapshot = capturePendingExternalAgentStage()
    if (!snapshot) return undefined
    const options = currentPendingACPOptions.value ?? (await ensurePendingACPRuntime(), currentPendingACPOptions.value)
    const pending = pendingExternalAgentSessionInput.value
    if (!options || !pending || !isPendingExternalAgentStageCurrent(snapshot)) return undefined
    if (!offered(options)) throw new Error('ACP option is not offered by the agent')
    pendingExternalAgentSessionInput.value = pick(pending)
    return pendingACPRuntimeStatus.value
  }

  // A model's reasoning efforts are known only once it is selected, so a
  // model pick reads that model's options; an effort it does not offer is
  // dropped.
  async function setPendingACPModel(modelId: string): Promise<AcpagentRuntimeStatus | undefined> {
    const pending = effectivePendingInput.value
    if (!pending) return
    const mid = modelId.trim()
    if (!mid) throw new Error('ACP model is not selected')
    const snapshot = capturePendingExternalAgentStage()
    if (!snapshot) return undefined
    const offered = currentPendingACPOptions.value ?? (await ensurePendingACPRuntime(), currentPendingACPOptions.value)
    if (!offered || !isPendingExternalAgentStageCurrent(snapshot)) return undefined
    if (!offered.models?.available_models?.some(model => model.id === mid)) throw new Error('ACP option is not offered by the agent')
    const options = await readACPOptions(snapshot.botId, pending, mid)
    const latest = pendingExternalAgentSessionInput.value
    if (!latest || !isPendingExternalAgentStageCurrent(snapshot)) return undefined
    pendingACPOptions.value = options
    const effort = latest.acpReasoningEffort
    const keptEffort = effort && options.reasoning?.available_efforts?.some(option => option.id === effort) ? effort : undefined
    pendingExternalAgentSessionInput.value = { ...latest, acpModelId: mid, acpReasoningEffort: keptEffort }
    return pendingACPRuntimeStatus.value
  }

  async function setPendingACPReasoning(effort: string): Promise<AcpagentRuntimeStatus | undefined> {
    if (!pendingExternalAgentSessionInput.value) return
    const value = effort.trim()
    if (!value) throw new Error('ACP reasoning effort is not selected')
    return pickPendingACP(
      options => !!options.reasoning?.available_efforts?.some(option => option.id === value),
      input => ({ ...input, acpReasoningEffort: value }),
    )
  }

  async function setPendingACPMode(modeId: string): Promise<AcpagentRuntimeStatus | undefined> {
    if (!pendingExternalAgentSessionInput.value) return
    if (!modeId.trim()) throw new Error('ACP mode is not selected')
    return pickPendingACP(
      options => !!options.modes?.available_modes?.some(mode => mode.id === modeId),
      input => ({ ...input, permissionMode: modeId }),
    )
  }

  function clearPendingExternalAgentSession() {
    nextPendingExternalAgentGeneration()
    clearPendingACPOptions()
    pendingExternalAgentSessionInput.value = null
    pendingExternalAgentBotId.value = ''
  }

  // Detaches the staged session, picks included, so another draft view can
  // take the composer and this one can be restored later.
  function detachPendingExternalAgentSession(): DetachedExternalAgentSession | null {
    const pending = pendingExternalAgentSessionInput.value
    if (!pending) return null
    const botId = pendingExternalAgentBotId.value
    const options = pendingACPOptions.value
    clearPendingExternalAgentSession()
    return { input: { ...pending }, botId, options }
  }

  function restorePendingExternalAgentSession(input: ExternalAgentSessionInput, botId: string, options?: AcpagentRuntimeStatus) {
    clearPendingACPOptions()
    pendingACPOptions.value = options
    pendingExternalAgentSessionInput.value = { ...input }
    pendingExternalAgentBotId.value = botId.trim()
  }

  function pendingExternalAgentMatchesInput(input: ExternalAgentSessionInput): boolean {
    const pending = pendingExternalAgentSessionInput.value
    if (!pending || sessionId.value) return false
    return sameExternalAgentSessionInput(pending, input)
  }

  return {
    pendingExternalAgentSessionInput,
    pendingExternalAgentSessionMetadata,
    effectiveDraftInput,
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
    pendingExternalAgentMatchesInput,
  }
}
