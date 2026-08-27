<template>
  <FormStack>
    <FieldStack :label="t('bots.schedule.execution.runsIn')">
      <Select v-model="runTargetModel">
        <SelectTrigger class="w-full">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="new_session">
            {{ t('bots.schedule.execution.newSession') }}
          </SelectItem>
          <SelectItem value="existing_session">
            {{ t('bots.schedule.execution.existingSession') }}
          </SelectItem>
        </SelectContent>
      </Select>
    </FieldStack>

    <FieldStack
      v-if="form.runTarget === 'new_session' && form.runtimeType === 'acp_agent'"
      :label="t('bots.settings.agentCredential')"
    >
      <Select v-model="credentialModel">
        <SelectTrigger class="w-full">
          <SelectValue :placeholder="t('bots.settings.agentCredentialPlaceholder')" />
        </SelectTrigger>
        <SelectContent>
          <SelectItem
            v-for="credential in agentCredentials"
            :key="credential.id"
            :value="credential.id || ''"
          >
            {{ credential.label }}
          </SelectItem>
        </SelectContent>
      </Select>
    </FieldStack>

    <FieldStack
      v-if="form.runTarget === 'existing_session'"
      :label="t('bots.schedule.execution.session')"
    >
      <!-- Only chat and schedule sessions can host a scheduled run; heartbeat,
           discuss and subagent threads back their own loops. -->
      <SessionSelect
        v-model="sessionModel"
        :bot-id="botId"
        :modes="TARGET_SESSION_MODES"
        :placeholder="t('bots.schedule.execution.sessionPlaceholder')"
        @update:session="selectedSession = $event"
      />
      <p
        v-if="selectedSession"
        class="text-caption text-muted-foreground"
      >
        {{ selectedSessionSummary }}
      </p>
    </FieldStack>

    <FieldStack :label="t('bots.schedule.execution.model')">
      <!-- Existing-session mode inherits the runtime; only the matching model
           column is offered. New-session mode picks the runtime here. Reasoning
           rides inside the picker that owns the model, the same way the chat
           composer folds the two into one decision. -->
      <ModelSelect
        v-if="form.runTarget === 'new_session'"
        v-model="runtimeModel"
        v-model:reasoning-effort="effortModel"
        :models="runtimePickerModels"
        :providers="runtimePickerProviders"
        model-type="chat"
        :placeholder="t('bots.schedule.execution.botDefault')"
        :show-reasoning="!acpAgentInPlay && nativeReasoningOptions.length > 0"
        :reasoning-options="nativeReasoningOptions"
      />
      <ModelSelect
        v-else-if="!selectedSessionIsACP"
        v-model="nativeModelModel"
        v-model:reasoning-effort="effortModel"
        :models="chatModels"
        :providers="providers"
        model-type="chat"
        :placeholder="t('bots.schedule.execution.sessionDefault')"
        :show-reasoning="nativeReasoningOptions.length > 0"
        :reasoning-options="nativeReasoningOptions"
      />
      <template v-if="acpAgentInPlay">
        <InlineLoadingRow v-if="acpCatalogLoading">
          {{ t('bots.schedule.execution.loadingAgentModels') }}
        </InlineLoadingRow>
        <p
          v-else-if="acpCatalogError"
          class="text-caption text-destructive"
        >
          {{ acpCatalogError }}
        </p>
        <ModelSelect
          v-else-if="acpCatalog"
          v-model="acpModelModel"
          v-model:reasoning-effort="effortModel"
          :models="acpPickerModels"
          :providers="NO_PROVIDERS"
          model-type="chat"
          :placeholder="t('bots.schedule.execution.agentDefaultModel')"
          :show-reasoning="acpReasoningOptions.length > 0"
          :reasoning-options="acpReasoningOptions"
        />
      </template>
    </FieldStack>

    <FieldStack
      v-if="form.runTarget === 'new_session' && selectableWorkdirs.length > 0"
      :label="t('bots.schedule.execution.workdir')"
    >
      <Select v-model="workdirModel">
        <SelectTrigger class="w-full">
          <SelectValue :placeholder="t('bots.schedule.execution.noWorkdir')" />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="default">
            {{ t('bots.schedule.execution.noWorkdir') }}
          </SelectItem>
          <SelectItem
            v-for="workdir in selectableWorkdirs"
            :key="workdir.id"
            :value="workdir.id ?? ''"
          >
            {{ workdir.name }} · {{ workdir.path }}
          </SelectItem>
        </SelectContent>
      </Select>
    </FieldStack>
  </FormStack>
</template>

<script setup lang="ts">
/* eslint-disable vue/no-mutating-props -- parent-owned reactive form, same
   contract as the settings-*-card children. */
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  FieldStack,
  FormStack,
  InlineLoadingRow,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@felinic/ui'
import {
  deleteBotsByBotIdAcpRuntimesByRuntimeId,
  getAcpProfiles,
  getBotsByBotIdAgentsByAgentIdCredentials,
  getBotsById,
  getModels,
  getProviders,
  postBotsByBotIdAcpRuntimes,
} from '@memohai/sdk'
import type {
  AcpclientModelInfo,
  AcpclientReasoningEffortInfo,
  AcpprofilePublicProfile,
  AgentcredentialPublicCredential,
  ModelsGetResponse,
  ProvidersGetResponse,
  SessionSession,
} from '@memohai/sdk'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { isACPAgentEnabled, normalizeACPAgentID } from '@/utils/acp'
import { normalizedRuntimeType } from '@/store/chat-list.utils'
import { useWorkdirsStore } from '@/store/workdirs'
import SessionSelect from '@/components/session-select/index.vue'
import ModelSelect from './model-select.vue'
import {
  EFFORT_LABELS,
  REASONING_EFFORT_DISABLE,
  availableEffortsForMode,
  nearestEffortToMedium,
  resolveEffortLevels,
  resolveThinkingMode,
} from './reasoning-effort'

// ScheduleExecutionForm is the editor-owned execution state this component
// mutates in place; the editor serializes it into the API execution block.
export interface ScheduleExecutionForm {
  runTarget: 'new_session' | 'existing_session'
  targetSessionId: string
  runtimeType: '' | 'acp_agent'
  acpAgentId: string
  agentCredentialId: string
  modelId: string
  acpModelId: string
  reasoningEffort: string
  workdirId: string
}

const props = defineProps<{
  botId: string
  form: ScheduleExecutionForm
}>()

const { t } = useI18n()
const workdirsStore = useWorkdirsStore()
const credentialModel = computed({
  get: () => props.form.agentCredentialId,
  set: (value: string) => { props.form.agentCredentialId = value },
})

// The backend only lets a schedule append to chat and schedule threads.
const TARGET_SESSION_MODES = ['chat', 'schedule']

// ACP agents ride the model picker as a synthetic provider group, so choosing
// a runtime and choosing a model stay one decision (and one search box).
const ACP_PROVIDER_ID = '__acp_agents__'
const ACP_VALUE_PREFIX = 'acp:'

// An agent's own models carry no Memoh provider, so their picker groups by
// nothing. Hoisted so the prop identity is stable across renders.
const NO_PROVIDERS: ProvidersGetResponse[] = []

const selectedSession = ref<SessionSession | null>(null)
const models = ref<ModelsGetResponse[]>([])
const providers = ref<ProvidersGetResponse[]>([])
const acpProfiles = ref<AcpprofilePublicProfile[]>([])
const botMetadata = ref<Record<string, unknown> | undefined>(undefined)

interface ACPCatalog {
  agentId: string
  models: AcpclientModelInfo[]
  efforts: AcpclientReasoningEffortInfo[]
  currentEffort: string
}
const acpCatalog = ref<ACPCatalog | null>(null)
const acpCatalogLoading = ref(false)
const acpCatalogError = ref<string | null>(null)
const agentCredentials = ref<AgentcredentialPublicCredential[]>([])

async function loadAgentCredentials(agentID: string) {
  agentID = normalizeACPAgentID(agentID)
  if (!agentID) {
    agentCredentials.value = []
    props.form.agentCredentialId = ''
    return
  }
  try {
    const { data } = await getBotsByBotIdAgentsByAgentIdCredentials({
      path: { bot_id: props.botId, agent_id: agentID },
      throwOnError: true,
    })
    agentCredentials.value = data?.items ?? []
    if (!props.form.agentCredentialId) {
      props.form.agentCredentialId = agentCredentials.value.find(item => item.is_default)?.id ?? ''
    }
  } catch {
    agentCredentials.value = []
    props.form.agentCredentialId = ''
  }
}

const chatModels = computed(() =>
  models.value.filter((m) => m.type === 'chat' && m.enable !== false),
)

const enabledAgents = computed(() =>
  acpProfiles.value.filter((profile) => isACPAgentEnabled(botMetadata.value, profile.id)),
)

const selectableWorkdirs = computed(() => {
  const live = workdirsStore.workdirsFor(props.botId).filter((wd) => !wd.archived && !!wd.id)
  // The ACP runtime lives in the native workspace; remote workdirs cannot
  // host it (same policy as session creation).
  return props.form.runtimeType === 'acp_agent'
    ? live.filter((wd) => wd.target_kind !== 'remote')
    : live
})

// Legacy rows carry the runtime in `type` with no `runtime_type`; resolve it
// the same way the backend does, or the form would offer the native model
// column for a session the API then rejects.
const selectedSessionIsACP = computed(() =>
  !!selectedSession.value && normalizedRuntimeType(selectedSession.value) === 'acp_agent',
)

const selectedSessionAgentID = computed(() => {
  if (!selectedSessionIsACP.value) return ''
  return normalizeACPAgentID(
    selectedSession.value?.runtime_metadata?.acp_agent_id ?? selectedSession.value?.metadata?.acp_agent_id,
  )
})

const selectedSessionSummary = computed(() => {
  if (!selectedSession.value) return ''
  if (selectedSessionIsACP.value) {
    const agent = acpProfiles.value.find((p) => p.id === selectedSessionAgentID.value)
    return t('bots.schedule.execution.sessionRuntimeAcp', { agent: agent?.display_name || selectedSessionAgentID.value })
  }
  return t('bots.schedule.execution.sessionRuntimeNative')
})

// acpAgentInPlay says whether the schedule executes through an ACP agent —
// either explicitly (new session with an agent) or inherited (existing ACP
// session) — and therefore which model/effort vocabulary applies.
const acpAgentInPlay = computed(() => {
  if (props.form.runTarget === 'new_session') return props.form.runtimeType === 'acp_agent'
  return selectedSessionIsACP.value
})

const activeAgentID = computed(() => {
  if (props.form.runTarget === 'new_session') return props.form.acpAgentId
  return selectedSessionAgentID.value
})

const runTargetModel = computed({
  get: () => props.form.runTarget,
  set: (value: string) => {
    const next = value === 'existing_session' ? 'existing_session' : 'new_session'
    if (next === props.form.runTarget) return
    // Switching mode invalidates the mode-specific fields wholesale.
    props.form.runTarget = next
    props.form.targetSessionId = ''
    props.form.runtimeType = ''
    props.form.acpAgentId = ''
    props.form.modelId = ''
    props.form.acpModelId = ''
    props.form.reasoningEffort = ''
    props.form.workdirId = ''
    selectedSession.value = null
  },
})

const sessionModel = computed({
  get: () => props.form.targetSessionId || '',
  set: (value: string) => {
    props.form.targetSessionId = value
    // Model overrides are runtime-specific; a new target session resets them.
    props.form.modelId = ''
    props.form.acpModelId = ''
    props.form.reasoningEffort = ''
  },
})

// The new-session picker folds runtime + model + agent into one list:
// '' (bot default) | '<model uuid>' | 'acp:<agent id>'.
const runtimePickerModels = computed<ModelsGetResponse[]>(() => [
  ...chatModels.value,
  ...enabledAgents.value.flatMap<ModelsGetResponse>((agent) => {
    const id = agent.id?.trim() ?? ''
    if (!id) return []
    return [{
      id: `${ACP_VALUE_PREFIX}${id}`,
      model_id: id,
      name: agent.display_name || id,
      provider_id: ACP_PROVIDER_ID,
      type: 'chat',
    }]
  }),
])

const runtimePickerProviders = computed<ProvidersGetResponse[]>(() => [
  ...providers.value,
  { id: ACP_PROVIDER_ID, name: t('bots.schedule.execution.acpAgents') },
])

const runtimeModel = computed({
  get: () => {
    if (props.form.runtimeType === 'acp_agent' && props.form.acpAgentId) return `${ACP_VALUE_PREFIX}${props.form.acpAgentId}`
    return props.form.modelId || ''
  },
  set: (value: string) => {
    props.form.modelId = ''
    props.form.acpModelId = ''
    props.form.reasoningEffort = ''
    props.form.agentCredentialId = ''
    if (value.startsWith(ACP_VALUE_PREFIX)) {
      props.form.runtimeType = 'acp_agent'
      props.form.acpAgentId = value.slice(ACP_VALUE_PREFIX.length)
    } else {
      props.form.runtimeType = ''
      props.form.acpAgentId = ''
      props.form.modelId = value
    }
    // A remote workdir cannot host the ACP runtime, so switching to an agent
    // can leave the current selection outside the offered list.
    if (props.form.workdirId && !selectableWorkdirs.value.some((wd) => wd.id === props.form.workdirId)) {
      props.form.workdirId = ''
    }
  },
})

const nativeModelModel = computed({
  get: () => props.form.modelId || '',
  set: (value: string) => {
    props.form.modelId = value
    props.form.reasoningEffort = ''
  },
})

const acpPickerModels = computed<ModelsGetResponse[]>(() =>
  (acpCatalog.value?.models ?? []).flatMap<ModelsGetResponse>((model) => {
    const id = model.id?.trim() ?? ''
    if (!id) return []
    return [{
      id,
      model_id: id,
      name: model.name?.trim() || id,
      provider_id: '',
      type: 'chat',
      config: { description: model.description?.trim() || undefined },
    }]
  }),
)

const acpModelModel = computed({
  get: () => props.form.acpModelId || '',
  set: (value: string) => {
    props.form.acpModelId = value
  },
})

const effortModel = computed({
  get: () => props.form.reasoningEffort,
  set: (value: string) => {
    props.form.reasoningEffort = value
  },
})

const workdirModel = computed({
  get: () => props.form.workdirId || 'default',
  set: (value: string) => {
    props.form.workdirId = value === 'default' ? '' : value
  },
})

// The selected native model's advertised tiers, plus the explicit "off" the
// composer offers. Without a model the bot/session default applies whole —
// there is nothing to override, so the picker shows no reasoning footer.
const nativeEffortTiers = computed<string[]>(() => {
  if (acpAgentInPlay.value || !props.form.modelId) return []
  const model = chatModels.value.find((m) => m.id === props.form.modelId)
  if (!model) return []
  const config = model.config as Parameters<typeof resolveThinkingMode>[0]
  if (resolveThinkingMode(config) === 'none') return []
  const clientType = providers.value.find((p) => p.id === model.provider_id)?.client_type
  return availableEffortsForMode(resolveThinkingMode(config), resolveEffortLevels(config, clientType))
})

const nativeReasoningOptions = computed<{ value: string; label: string }[]>(() =>
  nativeEffortTiers.value.map((effort) => ({
    value: effort,
    label: EFFORT_LABELS[effort] ? t(EFFORT_LABELS[effort]) : effort,
  })),
)

// ACP efforts are agent-defined; the agent reports its own set, and "off" is
// not part of that vocabulary.
const acpReasoningOptions = computed<{ value: string; label: string; description?: string }[]>(() =>
  (acpCatalog.value?.efforts ?? []).flatMap((effort) => {
    const value = effort.id?.trim() ?? ''
    if (!value) return []
    return [{
      value,
      label: effort.name?.trim() || value,
      description: effort.description?.trim() || undefined,
    }]
  }),
)

// The menus carry no "inherit" row, so whatever they show has to be what the
// schedule stores: an unset effort would render as the "off" tier and save as
// "follow the default". Seeding on the model/agent that owns the tiers keeps
// the two in step — the same thing the chat composer does when its model
// changes.
watch(nativeReasoningOptions, (options) => {
  if (options.length === 0) return
  const current = props.form.reasoningEffort
  if (current && options.some((option) => option.value === current)) return
  const tiers = nativeEffortTiers.value.filter((effort) => effort !== REASONING_EFFORT_DISABLE)
  props.form.reasoningEffort = tiers.includes('medium')
    ? 'medium'
    : nearestEffortToMedium(tiers) || REASONING_EFFORT_DISABLE
}, { immediate: true })

watch([acpReasoningOptions, acpAgentInPlay] as const, ([options, inPlay]) => {
  if (!inPlay || options.length === 0) return
  const current = props.form.reasoningEffort
  if (current && options.some((option) => option.value === current)) return
  const agentCurrent = (acpCatalog.value?.currentEffort ?? '').trim()
  props.form.reasoningEffort = options.some((option) => option.value === agentCurrent)
    ? agentCurrent
    : options[0]?.value ?? ''
}, { immediate: true })

// loadACPCatalog boots a temporary pre-session runtime — the only place an
// ACP agent's model and effort lists exist — reads them, and closes it.
async function loadACPCatalog(agentID: string) {
  if (!agentID || !props.botId) return
  if (acpCatalog.value?.agentId === agentID) return
  acpCatalogLoading.value = true
  acpCatalogError.value = null
  acpCatalog.value = null
  try {
    const { data } = await postBotsByBotIdAcpRuntimes({
      path: { bot_id: props.botId },
      body: { acp_agent_id: agentID, agent_credential_id: props.form.agentCredentialId || undefined },
      throwOnError: true,
    })
    acpCatalog.value = {
      agentId: agentID,
      models: data.models?.available_models ?? [],
      efforts: data.reasoning?.available_efforts ?? [],
      currentEffort: data.reasoning?.current_effort ?? '',
    }
    if (data.runtime_id) {
      void deleteBotsByBotIdAcpRuntimesByRuntimeId({
        path: { bot_id: props.botId, runtime_id: data.runtime_id },
      })
    }
  } catch (error) {
    acpCatalogError.value = resolveApiErrorMessage(error, t('bots.schedule.execution.agentModelsFailed'))
  } finally {
    acpCatalogLoading.value = false
  }
}

watch(activeAgentID, (agentID) => {
  if (agentID && acpAgentInPlay.value) {
    void loadAgentCredentials(agentID).then(() => loadACPCatalog(agentID))
  }
}, { immediate: true })

onMounted(async () => {
  void workdirsStore.ensureWorkdirs(props.botId)
  await Promise.all([
    (async () => {
      try {
        const { data } = await getModels({ throwOnError: true })
        models.value = data ?? []
      } catch { models.value = [] }
    })(),
    (async () => {
      try {
        const { data } = await getProviders({ throwOnError: true })
        providers.value = data ?? []
      } catch { providers.value = [] }
    })(),
    (async () => {
      try {
        const { data } = await getAcpProfiles({ throwOnError: true })
        acpProfiles.value = data.items ?? []
      } catch { acpProfiles.value = [] }
    })(),
    (async () => {
      try {
        const { data } = await getBotsById({ path: { id: props.botId }, throwOnError: true })
        botMetadata.value = (data as { metadata?: Record<string, unknown> }).metadata
      } catch { botMetadata.value = undefined }
    })(),
  ])
})
</script>
