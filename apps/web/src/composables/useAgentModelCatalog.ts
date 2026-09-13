import { computed, toValue, type MaybeRefOrGetter } from 'vue'
import { useQuery } from '@pinia/colada'
import {
  getBotsByBotIdAgentsByIdModels,
  getModels,
  getProviders,
  type AcpclientModelInfo,
  type AcpclientReasoningEffortInfo,
  type ExternalModelCatalog,
  type ExternalModelOption,
  type ModelsGetResponse,
  type ProvidersGetResponse,
} from '@memohai/sdk'
import {
  BOT_AGENT_RUNTIME_CLAUDE_CODE,
  BOT_AGENT_RUNTIME_CODEX,
} from '@/utils/bot-agent'

const ACP_SESSION_RUNTIME = 'acp_agent'

export function externalAgentModelsQueryKey(runtime: string, botId: string, botAgentId: string) {
  return ['external-agent-models', runtime, botId, botAgentId] as const
}

export interface AgentModelCatalog {
  models: ModelsGetResponse[]
  providers: ProvidersGetResponse[]
  configuredModelId: string
  configuredReasoningEffort: string
  defaultModelId: string
  defaultReasoningEffort: string
  reasoningEfforts?: AcpclientReasoningEffortInfo[]
  unavailablePermissionModes?: string[]
}

interface UseAgentModelCatalogOptions {
  botId: MaybeRefOrGetter<string | null | undefined>
  botAgentId: MaybeRefOrGetter<string | null | undefined>
  runtime: MaybeRefOrGetter<string | null | undefined>
  selectedModelId: MaybeRefOrGetter<string | null | undefined>
  projectPath: MaybeRefOrGetter<string | null | undefined>
  acpModels: MaybeRefOrGetter<AcpclientModelInfo[]>
  acpCurrentModelId: MaybeRefOrGetter<string | null | undefined>
  acpReasoningEfforts: MaybeRefOrGetter<AcpclientReasoningEffortInfo[]>
  acpCurrentReasoningEffort: MaybeRefOrGetter<string | null | undefined>
  acpLoading: MaybeRefOrGetter<boolean>
  refreshACP?: () => Promise<unknown>
}

function normalizePickerModels(
  models: Array<Pick<ExternalModelOption, 'id' | 'resolved_model_id' | 'name' | 'description'>>,
): ModelsGetResponse[] {
  return models.flatMap((model) => {
    const id = model.id?.trim() ?? ''
    if (!id) return []
    return [{
      id,
      model_id: model.resolved_model_id?.trim() || id,
      name: model.name?.trim() || id,
      provider_id: '',
      type: 'chat' as const,
      config: {
        description: model.description?.trim() || undefined,
      },
    }]
  })
}

function externalCatalog(
  source: ExternalModelCatalog | undefined,
  selectedModelId: string,
): AgentModelCatalog {
  const models = source?.models ?? []
  const defaultModelId = models.find(model => model.default)?.id?.trim() ?? ''
  const configuredModelId = source?.configured_model_id?.trim() ?? ''
  const effectiveModelId = selectedModelId || configuredModelId || defaultModelId
  const selectedModel = models.find(model => model.id?.trim() === effectiveModelId || model.resolved_model_id?.trim() === effectiveModelId)
  return {
    models: normalizePickerModels(models),
    providers: [],
    configuredModelId,
    configuredReasoningEffort: source?.configured_reasoning_effort?.trim() ?? '',
    defaultModelId,
    defaultReasoningEffort: selectedModel?.default_reasoning_effort?.trim() ?? '',
    reasoningEfforts: selectedModel?.reasoning_efforts ?? [],
    unavailablePermissionModes: selectedModel?.unavailable_permission_modes ?? [],
  }
}

// useAgentModelCatalog is the single frontend boundary for model discovery.
// Each runtime keeps its native transport, while consumers always receive the
// same picker-oriented catalog shape.
export function useAgentModelCatalog(options: UseAgentModelCatalogOptions) {
  const runtime = computed(() => toValue(options.runtime)?.trim() || 'model')
  const botId = computed(() => toValue(options.botId)?.trim() ?? '')
  const botAgentId = computed(() => toValue(options.botAgentId)?.trim() ?? '')
  const projectPath = computed(() => runtime.value === BOT_AGENT_RUNTIME_CLAUDE_CODE ? toValue(options.projectPath)?.trim() ?? '' : '')
  const isNative = computed(() => ![
    ACP_SESSION_RUNTIME,
    BOT_AGENT_RUNTIME_CODEX,
    BOT_AGENT_RUNTIME_CLAUDE_CODE,
  ].includes(runtime.value))

  const nativeModelsQuery = useQuery({
    key: ['models'],
    query: async () => {
      const { data } = await getModels({ throwOnError: true })
      return data
    },
    enabled: () => isNative.value,
  })
  const nativeProvidersQuery = useQuery({
    key: ['providers'],
    query: async () => {
      const { data } = await getProviders({ throwOnError: true })
      return data
    },
    enabled: () => isNative.value,
  })
  const selectedModelId = computed(() => toValue(options.selectedModelId)?.trim() ?? '')
  const defaultsModelId = computed(() => runtime.value === BOT_AGENT_RUNTIME_CLAUDE_CODE ? selectedModelId.value : '')
  const directScope = computed(() => JSON.stringify([runtime.value, botId.value, botAgentId.value, projectPath.value]))
  const directQuery = useQuery({
    key: () => [...externalAgentModelsQueryKey(runtime.value, botId.value, botAgentId.value), projectPath.value, defaultsModelId.value],
    query: async ({ signal }) => {
      const target = directScope.value
      const { data } = await getBotsByBotIdAgentsByIdModels({
        path: { bot_id: botId.value, id: botAgentId.value },
        query: { project_path: projectPath.value || undefined, model_id: defaultsModelId.value || undefined },
        signal,
        throwOnError: true,
      })
      return { target, catalog: data }
    },
    enabled: () => [BOT_AGENT_RUNTIME_CODEX, BOT_AGENT_RUNTIME_CLAUDE_CODE].includes(runtime.value) && !!botId.value && !!botAgentId.value,
    refetchOnWindowFocus: false,
    // Keep the same Agent's capabilities visible while resolving another
    // model's defaults; never carry a catalog across workspaces or Agents.
    placeholderData: previous => previous?.target === directScope.value ? previous : undefined,
  })

  const nativeModels = computed<ModelsGetResponse[]>(() => nativeModelsQuery.data.value ?? [])
  const catalog = computed<AgentModelCatalog>(() => {
    switch (runtime.value) {
      case ACP_SESSION_RUNTIME:
        return {
          models: normalizePickerModels(toValue(options.acpModels)),
          providers: [],
          configuredModelId: toValue(options.acpCurrentModelId)?.trim() ?? '',
          configuredReasoningEffort: toValue(options.acpCurrentReasoningEffort)?.trim() ?? '',
          defaultModelId: '',
          defaultReasoningEffort: '',
          reasoningEfforts: toValue(options.acpReasoningEfforts),
        }
      case BOT_AGENT_RUNTIME_CODEX:
      case BOT_AGENT_RUNTIME_CLAUDE_CODE:
        return externalCatalog(directQuery.data.value?.target === directScope.value ? directQuery.data.value.catalog : undefined, selectedModelId.value)
      default:
        return {
          models: nativeModels.value,
          providers: nativeProvidersQuery.data.value ?? [],
          configuredModelId: '',
          configuredReasoningEffort: '',
          defaultModelId: '',
          defaultReasoningEffort: '',
        }
    }
  })

  const isLoading = computed(() => {
    switch (runtime.value) {
      case ACP_SESSION_RUNTIME:
        return toValue(options.acpLoading)
      case BOT_AGENT_RUNTIME_CODEX:
      case BOT_AGENT_RUNTIME_CLAUDE_CODE:
        return directQuery.isLoading.value
      default:
        return nativeModelsQuery.isLoading.value || nativeProvidersQuery.isLoading.value
    }
  })
  const error = computed(() => {
    switch (runtime.value) {
      case BOT_AGENT_RUNTIME_CODEX:
      case BOT_AGENT_RUNTIME_CLAUDE_CODE:
        return directQuery.error.value
      case ACP_SESSION_RUNTIME:
        return null
      default:
        return nativeModelsQuery.error.value ?? nativeProvidersQuery.error.value
    }
  })

  async function refresh() {
    switch (runtime.value) {
      case ACP_SESSION_RUNTIME:
        await options.refreshACP?.()
        return
      case BOT_AGENT_RUNTIME_CODEX:
      case BOT_AGENT_RUNTIME_CLAUDE_CODE:
        await directQuery.refetch()
        return
      default:
        await Promise.all([nativeModelsQuery.refetch(), nativeProvidersQuery.refetch()])
    }
  }

  return {
    catalog,
    nativeModels,
    isLoading,
    error,
    refresh,
  }
}
