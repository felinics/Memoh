import { ref, type Ref } from 'vue'
import type { AcpagentRuntimeStatus } from '@memohai/sdk'
import {
  ensureACPRuntime as requestEnsureACPRuntime,
  setACPRuntimeMode as requestSetACPRuntimeMode,
  setACPRuntimeModel as requestSetACPRuntimeModel,
  setACPRuntimeReasoning as requestSetACPRuntimeReasoning,
} from '@/composables/api/useChat'

export interface ACPRuntimeStatusRegistry {
  acpRuntimeStatuses: Ref<Record<string, AcpagentRuntimeStatus | undefined>>
  acpRuntimeKey: (botId: string, sessionId: string) => string
  setACPRuntimeStatus: (botId: string, sessionId: string, runtime: AcpagentRuntimeStatus | undefined) => void
  clearACPRuntimeStatus: (botId: string, sessionId: string) => void
}

export interface ACPRuntimeRegistryTransport {
  ensureACPRuntime: typeof requestEnsureACPRuntime
  setACPRuntimeMode: typeof requestSetACPRuntimeMode
  setACPRuntimeModel: typeof requestSetACPRuntimeModel
  setACPRuntimeReasoning: typeof requestSetACPRuntimeReasoning
}

interface ACPRuntimeRegistryDeps {
  currentBotId: Ref<string | null>
  sessionId: Ref<string | null>
}

const defaultTransport: ACPRuntimeRegistryTransport = {
  ensureACPRuntime: requestEnsureACPRuntime,
  setACPRuntimeMode: requestSetACPRuntimeMode,
  setACPRuntimeModel: requestSetACPRuntimeModel,
  setACPRuntimeReasoning: requestSetACPRuntimeReasoning,
}

export function createACPRuntimeRegistry(
  { currentBotId, sessionId }: ACPRuntimeRegistryDeps,
  transport: ACPRuntimeRegistryTransport = defaultTransport,
) {
  const acpRuntimeStatuses = ref<Record<string, AcpagentRuntimeStatus | undefined>>({})
  const acpRuntimePending = ref<Record<string, boolean>>({})
  const requests = new Map<string, Promise<AcpagentRuntimeStatus | undefined>>()
  const statusVersions = new Map<string, number>()
  let registryGeneration = 0

  function bumpStatusVersion(key: string) {
    const next = (statusVersions.get(key) ?? 0) + 1
    statusVersions.set(key, next)
    return next
  }

  function acpRuntimeKey(botId: string, targetSessionId: string) {
    const bid = botId.trim()
    const sid = targetSessionId.trim()
    return bid && sid ? `${bid}:${sid}` : ''
  }

  function setACPRuntimeStatus(botId: string, targetSessionId: string, runtime: AcpagentRuntimeStatus | undefined) {
    const key = acpRuntimeKey(botId, targetSessionId)
    if (!key) return
    const next = { ...acpRuntimeStatuses.value }
    if (runtime) next[key] = runtime
    else delete next[key]
    acpRuntimeStatuses.value = next
  }

  function setACPRuntimePending(botId: string, targetSessionId: string, pending: boolean) {
    const key = acpRuntimeKey(botId, targetSessionId)
    if (!key) return
    const next = { ...acpRuntimePending.value }
    if (pending) next[key] = true
    else delete next[key]
    acpRuntimePending.value = next
  }

  function clearACPRuntimeStatus(botId: string, targetSessionId: string) {
    const key = acpRuntimeKey(botId, targetSessionId)
    if (!key) return
    requests.delete(key)
    bumpStatusVersion(key)
    setACPRuntimeStatus(botId, targetSessionId, undefined)
    setACPRuntimePending(botId, targetSessionId, false)
  }

  async function ensureACPRuntimeFor(botID: string, sessionID: string): Promise<AcpagentRuntimeStatus | undefined> {
    const bid = botID.trim()
    const sid = sessionID.trim()
    if (!bid || !sid) throw new Error('ACP session is not selected')
    const key = acpRuntimeKey(bid, sid)
    const existing = requests.get(key)
    if (existing) return existing

    const generation = registryGeneration
    const statusVersion = statusVersions.get(key) ?? 0
    setACPRuntimePending(bid, sid, true)
    const request = transport.ensureACPRuntime(bid, sid)
      .then((runtime) => {
        if (
          registryGeneration !== generation
          || (statusVersions.get(key) ?? 0) !== statusVersion
          || requests.get(key) !== request
        ) {
          return acpRuntimeStatuses.value[key]
        }
        setACPRuntimeStatus(bid, sid, runtime)
        return runtime
      })
      .finally(() => {
        if (requests.get(key) !== request) return
        requests.delete(key)
        setACPRuntimePending(bid, sid, false)
      })
    requests.set(key, request)
    return request
  }

  async function ensureACPRuntime(sessionID?: string): Promise<AcpagentRuntimeStatus | undefined> {
    const bid = currentBotId.value?.trim() ?? ''
    const sid = sessionID?.trim() || sessionId.value?.trim() || ''
    return ensureACPRuntimeFor(bid, sid)
  }

  async function refreshACPRuntimeFor(botID: string, sessionID: string): Promise<AcpagentRuntimeStatus | undefined> {
    const key = acpRuntimeKey(botID, sessionID)
    if (!key) throw new Error('ACP session is not selected')
    // Keep the last accepted snapshot visible while invalidating any passive
    // request that started before a command changed the live Agent state.
    requests.delete(key)
    bumpStatusVersion(key)
    return ensureACPRuntimeFor(botID, sessionID)
  }

  async function updateACPRuntimeFor(
    botID: string,
    sessionID: string,
    update: (botId: string, sessionId: string) => Promise<AcpagentRuntimeStatus>,
  ): Promise<AcpagentRuntimeStatus | undefined> {
    const bid = botID.trim()
    const sid = sessionID.trim()
    if (!bid || !sid) throw new Error('ACP session is not selected')
    const key = acpRuntimeKey(bid, sid)
    const generation = registryGeneration
    const statusVersion = bumpStatusVersion(key)
    let runtime: AcpagentRuntimeStatus
    try {
      runtime = await update(bid, sid)
    } catch (error) {
      if (
        registryGeneration === generation
        && statusVersions.get(key) === statusVersion
      ) bumpStatusVersion(key)
      throw error
    }
    if (
      registryGeneration !== generation
      || statusVersions.get(key) !== statusVersion
    ) {
      return acpRuntimeStatuses.value[key]
    }
    bumpStatusVersion(key)
    setACPRuntimeStatus(bid, sid, runtime)
    return runtime
  }

  async function setACPRuntimeModelFor(botID: string, sessionID: string, modelID: string): Promise<AcpagentRuntimeStatus | undefined> {
    const mid = modelID.trim()
    if (!mid) throw new Error('ACP model is not selected')
    return updateACPRuntimeFor(botID, sessionID, (bid, sid) => transport.setACPRuntimeModel(bid, sid, mid))
  }

  async function setACPRuntimeModel(modelID: string, sessionID?: string): Promise<AcpagentRuntimeStatus | undefined> {
    const bid = currentBotId.value?.trim() ?? ''
    const sid = sessionID?.trim() || sessionId.value?.trim() || ''
    return setACPRuntimeModelFor(bid, sid, modelID)
  }

  async function setACPRuntimeModeFor(botID: string, sessionID: string, modeID: string): Promise<AcpagentRuntimeStatus | undefined> {
    if (!modeID) throw new Error('ACP mode is not selected')
    return updateACPRuntimeFor(botID, sessionID, (bid, sid) => transport.setACPRuntimeMode(bid, sid, modeID))
  }

  async function setACPRuntimeMode(modeID: string, sessionID?: string): Promise<AcpagentRuntimeStatus | undefined> {
    const bid = currentBotId.value?.trim() ?? ''
    const sid = sessionID?.trim() || sessionId.value?.trim() || ''
    return setACPRuntimeModeFor(bid, sid, modeID)
  }

  async function setACPRuntimeReasoningFor(botID: string, sessionID: string, effort: string): Promise<AcpagentRuntimeStatus | undefined> {
    const value = effort.trim()
    if (!value) throw new Error('ACP reasoning effort is not selected')
    return updateACPRuntimeFor(botID, sessionID, (bid, sid) => transport.setACPRuntimeReasoning(bid, sid, value))
  }

  async function setACPRuntimeReasoning(effort: string, sessionID?: string): Promise<AcpagentRuntimeStatus | undefined> {
    const bid = currentBotId.value?.trim() ?? ''
    const sid = sessionID?.trim() || sessionId.value?.trim() || ''
    return setACPRuntimeReasoningFor(bid, sid, effort)
  }

  function resetExternalAgentRuntimeRegistry() {
    registryGeneration += 1
    requests.clear()
    statusVersions.clear()
    acpRuntimeStatuses.value = {}
    acpRuntimePending.value = {}
  }

  return {
    acpRuntimeStatuses,
    acpRuntimePending,
    acpRuntimeKey,
    setACPRuntimeStatus,
    clearACPRuntimeStatus,
    ensureACPRuntimeFor,
    ensureACPRuntime,
    refreshACPRuntimeFor,
    setACPRuntimeModeFor,
    setACPRuntimeMode,
    setACPRuntimeModelFor,
    setACPRuntimeModel,
    setACPRuntimeReasoningFor,
    setACPRuntimeReasoning,
    resetExternalAgentRuntimeRegistry,
  }
}
