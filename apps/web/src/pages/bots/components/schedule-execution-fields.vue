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
      v-if="form.runTarget === 'existing_session'"
      :label="t('bots.schedule.execution.session')"
    >
      <!-- Only chat and schedule sessions can host a scheduled run; discuss
           and subagent threads back their own loops. -->
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

    <FieldStack
      :label="t('bots.schedule.execution.model')"
      :help="modelHelp"
    >
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
        :placeholder="newSessionPlaceholder"
        :show-reasoning="!acpAgentInPlay && nativeReasoningOptions.length > 0"
        :reasoning-options="nativeReasoningOptions"
      />
      <ModelSelect
        v-else-if="!selectedSessionIsExternalAgent"
        v-model="nativeModelModel"
        v-model:reasoning-effort="effortModel"
        :models="chatModels"
        :providers="providers"
        model-type="chat"
        :placeholder="t('bots.schedule.execution.sessionDefault')"
        :show-reasoning="nativeReasoningOptions.length > 0"
        :reasoning-options="nativeReasoningOptions"
      />
      <p
        v-else-if="!selectedSessionIsACP"
        class="text-caption text-muted-foreground"
      >
        {{ t('bots.schedule.execution.agentDefaultModel') }}
      </p>
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
  getBotsByBotIdAgents,
  getBotsByBotIdSettings,
  getModels,
  getProviders,
  postBotsByBotIdAcpRuntimes,
} from '@memohai/sdk'
import type {
  AcpclientModelInfo,
  AcpclientReasoningEffortInfo,
  AcpprofilePublicProfile,
  BotagentsBotAgent,
  ModelsGetResponse,
  ProvidersGetResponse,
  SessionSession,
} from '@memohai/sdk'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { normalizeACPAgentID } from '@/utils/acp'
import { BOT_AGENT_RUNTIME_CLAUDE_CODE, BOT_AGENT_RUNTIME_CODEX, botAgentName, botAgentProvider, normalizeBotAgentRuntime } from '@/utils/bot-agent'
import { isAgentRuntimeType, normalizedRuntimeType } from '@/store/chat-list.utils'
import { useWorkdirsStore } from '@/store/workdirs'
import SessionSelect from '@/components/session-select/index.vue'
import ModelSelect from './model-select.vue'
import {
  EFFORT_LABELS,
  REASONING_EFFORT_DISABLE,
  reconcileStoredEffort,
  selectableEfforts,
} from './reasoning-effort'

// ScheduleExecutionForm is the editor-owned execution state this component
// mutates in place; the editor serializes it into the API execution block.
export interface ScheduleExecutionForm {
  runTarget: 'new_session' | 'existing_session'
  targetSessionId: string
  runtimeType: '' | 'acp_agent' | 'codex' | 'claude-code'
  botAgentId: string
  acpAgentId: string
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

// The backend only lets a schedule append to chat and schedule threads.
const TARGET_SESSION_MODES = ['chat', 'schedule']

// External Agents ride the model picker as a synthetic provider group, so choosing
// a runtime and choosing a model stay one decision (and one search box).
const EXTERNAL_AGENT_PROVIDER_ID = '__external_agents__'
const EXTERNAL_AGENT_VALUE_PREFIX = 'agent:'

// An agent's own models carry no Memoh provider, so their picker groups by
// nothing. Hoisted so the prop identity is stable across renders.
const NO_PROVIDERS: ProvidersGetResponse[] = []

const selectedSession = ref<SessionSession | null>(null)
// undefined until the settings request settles, so the model field never
// flashes a "you must pick one" state at a bot that does have a default.
const botDefaultModelID = ref<string | undefined>(undefined)
const models = ref<ModelsGetResponse[]>([])
const providers = ref<ProvidersGetResponse[]>([])
const acpProfiles = ref<AcpprofilePublicProfile[]>([])
const botAgents = ref<BotagentsBotAgent[]>([])

interface ACPCatalog {
  agentId: string
  botAgentId: string
  models: AcpclientModelInfo[]
  efforts: AcpclientReasoningEffortInfo[]
  currentEffort: string
}
const acpCatalog = ref<ACPCatalog | null>(null)
const acpCatalogLoading = ref(false)
const acpCatalogError = ref<string | null>(null)
const chatModels = computed(() =>
  models.value.filter((m) => m.type === 'chat' && m.enable !== false),
)

const enabledAgents = computed(() => botAgents.value.filter(agent => agent.enabled !== false && !!agent.id))

function sessionRuntimeForAgent(agent: BotagentsBotAgent | undefined): 'acp_agent' | 'codex' | 'claude-code' {
  const runtime = normalizeBotAgentRuntime(agent?.runtime)
  if (runtime === BOT_AGENT_RUNTIME_CODEX || runtime === BOT_AGENT_RUNTIME_CLAUDE_CODE) return runtime
  return 'acp_agent'
}

const selectableWorkdirs = computed(() => {
  const live = workdirsStore.workdirsFor(props.botId).filter((wd) => !wd.archived && !!wd.id)
  return isAgentRuntimeType(props.form.runtimeType)
    ? live.filter((wd) => wd.target_kind !== 'remote')
    : live
})

// Legacy rows carry the runtime in `type` with no `runtime_type`; resolve it
// the same way the backend does, or the form would offer the native model
// column for a session the API then rejects.
const selectedSessionIsACP = computed(() =>
  !!selectedSession.value && normalizedRuntimeType(selectedSession.value) === 'acp_agent',
)
const selectedSessionIsExternalAgent = computed(() =>
  !!selectedSession.value && isAgentRuntimeType(normalizedRuntimeType(selectedSession.value)),
)

const selectedSessionAgentID = computed(() => {
  if (!selectedSessionIsACP.value) return ''
  return normalizeACPAgentID(
    selectedSession.value?.runtime_metadata?.acp_agent_id ?? selectedSession.value?.metadata?.acp_agent_id,
  )
})

const selectedSessionSummary = computed(() => {
  if (!selectedSession.value) return ''
  if (selectedSessionIsExternalAgent.value) {
    const botAgent = botAgents.value.find(agent => agent.id === selectedSession.value?.bot_agent_id)
    const profile = acpProfiles.value.find(item => normalizeACPAgentID(item.id) === selectedSessionAgentID.value)
    return t('bots.schedule.execution.sessionRuntimeAgent', { agent: botAgent ? botAgentName(botAgent) : (profile?.display_name || selectedSessionAgentID.value) })
  }
  return t('bots.schedule.execution.sessionRuntimeNative')
})

const externalAgentInPlay = computed(() => {
  if (props.form.runTarget === 'new_session') return isAgentRuntimeType(props.form.runtimeType)
  return selectedSessionIsExternalAgent.value
})

const acpAgentInPlay = computed(() => {
  if (props.form.runTarget === 'new_session') return props.form.runtimeType === 'acp_agent'
  return selectedSessionIsACP.value
})

const activeAgentID = computed(() => {
  if (props.form.runTarget === 'new_session') return props.form.acpAgentId
  return selectedSessionAgentID.value
})

// Two instances of the same provider can hold different accounts, so the
// catalog identity must include the instance, not just the provider.
const activeBotAgentID = computed(() => {
  if (props.form.runTarget === 'new_session') return props.form.botAgentId
  return ''
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
    props.form.botAgentId = ''
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
// '' (bot default) | '<model uuid>' | 'agent:<BotAgent uuid>'.
const runtimePickerModels = computed<ModelsGetResponse[]>(() => [
  ...chatModels.value,
  ...enabledAgents.value.flatMap<ModelsGetResponse>((agent) => {
    const id = agent.id?.trim() ?? ''
    if (!id) return []
    return [{
      id: `${EXTERNAL_AGENT_VALUE_PREFIX}${id}`,
      model_id: id,
      name: botAgentName(agent),
      provider_id: EXTERNAL_AGENT_PROVIDER_ID,
      type: 'chat',
    }]
  }),
])

const runtimePickerProviders = computed<ProvidersGetResponse[]>(() => [
  ...providers.value,
  { id: EXTERNAL_AGENT_PROVIDER_ID, name: t('bots.schedule.execution.externalAgents') },
])

// Model selection falls back bot default → the session's last round, and a
// fresh session per fire has no last round. So a bot with no default leaves a
// scheduled run with nothing to resolve, and the backend rejects it. The form
// closes that hole by picking a model itself rather than offering a "bot
// default" that does not exist.
const modelRequired = computed(() =>
  props.form.runTarget === 'new_session'
  && botDefaultModelID.value !== undefined
  && botDefaultModelID.value === ''
  && !externalAgentInPlay.value,
)

const newSessionPlaceholder = computed(() =>
  modelRequired.value
    ? t('bots.schedule.execution.pickModel')
    : t('bots.schedule.execution.botDefault'),
)

const modelHelp = computed(() => {
  if (!modelRequired.value) return ''
  return chatModels.value.length === 0
    ? t('bots.schedule.execution.noModelAvailable')
    : t('bots.schedule.execution.noBotDefaultModel')
})

// Seed the picker once the lists land, so the field is answered rather than
// blocking on a question the user did not ask to be asked. It stays a normal
// selection they can change.
watch([modelRequired, chatModels], ([required, available]) => {
  if (!required || props.form.modelId || available.length === 0) return
  props.form.modelId = available[0]?.id ?? ''
}, { immediate: true })

const runtimeModel = computed({
  get: () => {
    if (isAgentRuntimeType(props.form.runtimeType) && props.form.botAgentId) return `${EXTERNAL_AGENT_VALUE_PREFIX}${props.form.botAgentId}`
    return props.form.modelId || ''
  },
  set: (value: string) => {
    props.form.modelId = ''
    props.form.acpModelId = ''
    props.form.reasoningEffort = ''
    if (value.startsWith(EXTERNAL_AGENT_VALUE_PREFIX)) {
      const botAgentId = value.slice(EXTERNAL_AGENT_VALUE_PREFIX.length)
      const agent = enabledAgents.value.find(item => item.id === botAgentId)
      props.form.runtimeType = sessionRuntimeForAgent(agent)
      props.form.botAgentId = botAgentId
      props.form.acpAgentId = props.form.runtimeType === 'acp_agent' ? botAgentProvider(agent) : ''
    } else {
      props.form.runtimeType = ''
      props.form.botAgentId = ''
      props.form.acpAgentId = ''
      props.form.modelId = value
    }
    // A remote workdir cannot host an External Agent, so switching to an agent
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

// The selected native model's tiers, as the server resolved them. The client
// no longer derives efforts from raw model config — that duplication is what
// let the picker and the wire disagree. Without a model the bot/session
// default applies whole, so the picker shows no reasoning footer.
const nativeReasoning = computed(() => {
  if (externalAgentInPlay.value || !props.form.modelId) return null
  const model = chatModels.value.find((m) => m.id === props.form.modelId)
  return model?.reasoning ?? null
})

const nativeEffortTiers = computed<string[]>(() => selectableEfforts(nativeReasoning.value))

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
  // The server's resolved default is the seed; reconcileStoredEffort also maps
  // a stale stored tier onto it, mirroring what the chat composer does.
  props.form.reasoningEffort =
    reconcileStoredEffort(current ?? '', nativeReasoning.value) || REASONING_EFFORT_DISABLE
}, { immediate: true })

watch([acpReasoningOptions, acpAgentInPlay] as const, ([options, inPlay]) => {
  if (!inPlay || options.length === 0) return
  const current = props.form.reasoningEffort
  if (current && options.some((option) => option.value === current)) return
  const agentCurrent = (acpCatalog.value?.currentEffort ?? '').trim()
  props.form.reasoningEffort = options.some((option) => option.value === agentCurrent)
    ? agentCurrent
    : (options[0]?.value ?? '')
}, { immediate: true })

// loadACPCatalog boots a temporary pre-session runtime — the only place an
// ACP agent's model and effort lists exist — reads them, and closes it.
async function loadACPCatalog(agentID: string) {
  if (!agentID || !props.botId) return
  const botAgentID = activeBotAgentID.value
  if (acpCatalog.value?.agentId === agentID && acpCatalog.value?.botAgentId === botAgentID) return
  acpCatalogLoading.value = true
  acpCatalogError.value = null
  acpCatalog.value = null
  try {
    const { data } = await postBotsByBotIdAcpRuntimes({
      path: { bot_id: props.botId },
      body: { acp_agent_id: agentID },
      throwOnError: true,
    })
    acpCatalog.value = {
      agentId: agentID,
      botAgentId: botAgentID,
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

watch([activeAgentID, activeBotAgentID], ([agentID]) => {
  if (agentID && acpAgentInPlay.value) {
    void loadACPCatalog(agentID)
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
        const { data } = await getBotsByBotIdAgents({ path: { bot_id: props.botId }, throwOnError: true })
        botAgents.value = data.items ?? []
      } catch { botAgents.value = [] }
    })(),
    (async () => {
      try {
        const { data } = await getBotsByBotIdSettings({ path: { bot_id: props.botId }, throwOnError: true })
        botDefaultModelID.value = (data as { chat_model_id?: string } | undefined)?.chat_model_id?.trim() ?? ''
      } catch {
        // Unknown, not "absent": leaving it undefined keeps the field on its
        // normal "bot default" reading rather than demanding a model on a
        // transient settings error.
        botDefaultModelID.value = undefined
      }
    })(),
  ])
})

// The editor gates Save on this: everywhere else the form seeds a model
// itself, so it only reads false when the bot has no default AND there is no
// chat model to seed from.
defineExpose({
  modelSatisfied: computed(() => !modelRequired.value || !!props.form.modelId),
})
</script>
