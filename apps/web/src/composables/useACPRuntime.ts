import { computed, ref, shallowRef, toValue, watch, type MaybeRefOrGetter } from 'vue'
import { storeToRefs } from 'pinia'
import { useChatStore } from '@/store/chat-list'
import type { ChatViewTarget } from '@/store/chat/types'
import { acpRuntimeMatchesConfiguration } from '@/utils/acp-slash-commands'
import type { AcpagentRuntimeStatus, AcpclientAvailableCommandInfo, AcpclientModeInfo, AcpclientModelInfo, AcpclientReasoningEffortInfo } from '@memohai/sdk'

interface UseACPRuntimeOptions {
  target: MaybeRefOrGetter<ChatViewTarget>
  pending: MaybeRefOrGetter<boolean>
  enabled: MaybeRefOrGetter<boolean>
  agentId: MaybeRefOrGetter<string>
  projectPath: MaybeRefOrGetter<string>
}

export function useACPRuntime(options: UseACPRuntimeOptions) {
  const chatStore = useChatStore()
  const { acpRuntimeStatuses, acpRuntimePending } = storeToRefs(chatStore)

  const target = computed(() => {
    const value = toValue(options.target)
    return {
      botId: value.botId.trim(),
      sessionId: value.sessionId?.trim() || null,
      viewId: value.viewId.trim() || 'chat',
    }
  })
  const pendingState = computed(() => {
    if (!toValue(options.enabled) || !toValue(options.pending)) return null
    return chatStore.pendingExternalAgentStateFor(target.value)
  })
  const agentId = computed(() => toValue(options.agentId).trim())
  const projectPath = computed(() => toValue(options.projectPath).trim())
  const key = computed(() => {
    if (!toValue(options.enabled) || toValue(options.pending)) return ''
    const { botId, sessionId } = target.value
    if (!botId || !sessionId) return ''
    return chatStore.acpRuntimeKey(botId, sessionId)
  })

  function belongsToRuntime(status: AcpagentRuntimeStatus | undefined) {
    return acpRuntimeMatchesConfiguration(status, agentId.value, projectPath.value)
  }

  const observedRuntime = computed(() => {
    const status = toValue(options.pending)
      ? pendingState.value?.runtimeStatus
      : key.value
        ? acpRuntimeStatuses.value[key.value]
        : undefined
    return belongsToRuntime(status) ? status : undefined
  })
  // The registry is the canonical full snapshot for this Session. Composer
  // drafts (model/reasoning overrides) live separately; never splice controls
  // from a replacement runtime into the previous runtime identity.
  const runtime = shallowRef<AcpagentRuntimeStatus>()
  const isPreparing = ref(false)
  let requestVersion = 0
  let preparingVersion = 0
  const isEnsuring = computed(() => {
    if (toValue(options.pending)) return pendingState.value?.ensuring ?? false
    return key.value ? !!acpRuntimePending.value[key.value] : false
  })
  const models = computed<AcpclientModelInfo[]>(() => runtime.value?.models?.available_models ?? [])
  const currentModelId = computed(() => runtime.value?.models?.current_model_id ?? '')
  const modes = computed<Array<AcpclientModeInfo & { id: string }>>(() =>
    (runtime.value?.modes?.available_modes ?? []).filter(
      (mode): mode is AcpclientModeInfo & { id: string } =>
        typeof mode.id === 'string' && mode.id.trim() !== '',
    ),
  )
  const currentModeId = computed(() => runtime.value?.modes?.current_mode_id ?? '')
  const availableCommands = computed<AcpclientAvailableCommandInfo[]>(() => runtime.value?.available_commands ?? [])
  const reasoningEfforts = computed<AcpclientReasoningEffortInfo[]>(() => runtime.value?.reasoning?.available_efforts ?? [])
  const currentReasoningEffort = computed(() => runtime.value?.reasoning?.current_effort ?? '')

  function requestScope() {
    const value = target.value
    return JSON.stringify([
      toValue(options.enabled),
      toValue(options.pending),
      value.botId,
      value.sessionId,
      value.viewId,
      agentId.value,
      projectPath.value,
    ])
  }

  function requestIsCurrent(version: number, scope: string) {
    return version === requestVersion
      && toValue(options.enabled)
      && requestScope() === scope
  }

  async function ensureObservedRuntime() {
    if (!toValue(options.enabled)) return undefined
    if (toValue(options.pending)) return chatStore.ensurePendingACPRuntime(target.value)
    const sid = target.value.sessionId ?? ''
    if (!sid) return undefined
    return chatStore.ensureACPRuntime(sid)
  }

  async function setObservedModel(modelId: string) {
    if (toValue(options.pending)) return chatStore.setPendingACPModel(modelId, target.value)
    const sid = target.value.sessionId ?? ''
    return chatStore.setACPRuntimeModel(modelId, sid)
  }

  async function setObservedMode(modeId: string) {
    if (toValue(options.pending)) return chatStore.setPendingACPMode(modeId, target.value)
    const sid = target.value.sessionId ?? ''
    if (!sid) return undefined
    return chatStore.setACPRuntimeMode(modeId, sid)
  }

  async function setObservedReasoning(effort: string) {
    if (toValue(options.pending)) return chatStore.setPendingACPReasoning(effort, target.value)
    const sid = target.value.sessionId ?? ''
    return chatStore.setACPRuntimeReasoning(effort, sid)
  }

  function adopt(runtimeStatus: AcpagentRuntimeStatus | undefined, version: number, scope: string) {
    if (
      !runtimeStatus
      || !requestIsCurrent(version, scope)
      || !belongsToRuntime(runtimeStatus)
    ) return undefined
    runtime.value = runtimeStatus
    return runtimeStatus
  }

  async function ensure(markPreparing = false, desiredModelId = '') {
    if (!toValue(options.enabled)) return undefined
    const version = ++requestVersion
    const scope = requestScope()
    if (markPreparing) {
      preparingVersion = version
      isPreparing.value = true
    }
    try {
      // Capabilities are Agent-owned dynamic state. Ensure them when the ACP
      // composer becomes visible and refresh them when the picker opens.
      let status = adopt(await ensureObservedRuntime(), version, scope)
      const modelId = desiredModelId.trim()
      const modelAvailable = status?.models?.available_models?.some(
        model => model.id?.trim() === modelId,
      )
      if (status && modelId && modelAvailable && status.models?.current_model_id?.trim() !== modelId) {
        status = adopt(await setObservedModel(modelId), version, scope)
      }
      return status
    } catch (error) {
      if (!requestIsCurrent(version, scope)) return undefined
      throw error
    } finally {
      if (markPreparing && version === preparingVersion) {
        preparingVersion = 0
        isPreparing.value = false
      }
    }
  }

  async function setModel(modelId: string) {
    const version = ++requestVersion
    const scope = requestScope()
    try {
      return adopt(await setObservedModel(modelId), version, scope)
    } catch (error) {
      if (!requestIsCurrent(version, scope)) return undefined
      throw error
    }
  }

  async function setMode(modeId: string) {
    const version = ++requestVersion
    const scope = requestScope()
    try {
      return adopt(await setObservedMode(modeId), version, scope)
    } catch (error) {
      if (!requestIsCurrent(version, scope)) return undefined
      throw error
    }
  }

  async function setReasoning(effort: string) {
    const version = ++requestVersion
    const scope = requestScope()
    try {
      return adopt(await setObservedReasoning(effort), version, scope)
    } catch (error) {
      if (!requestIsCurrent(version, scope)) return undefined
      throw error
    }
  }

  watch(requestScope, (scope, previousScope) => {
    if (scope === previousScope) return
    requestVersion += 1
    preparingVersion = 0
    runtime.value = toValue(options.enabled) ? observedRuntime.value : undefined
    isPreparing.value = false
  }, { immediate: true })

  watch(observedRuntime, (status) => {
    runtime.value = toValue(options.enabled) ? status : undefined
  }, { immediate: true })

  return {
    runtime,
    modes,
    currentModeId,
    availableCommands,
    models,
    currentModelId,
    reasoningEfforts,
    currentReasoningEffort,
    isEnsuring,
    isPreparing,
    ensure,
    setMode,
    setModel,
    setReasoning,
  }
}
