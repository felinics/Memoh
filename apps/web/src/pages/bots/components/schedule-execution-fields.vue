<template>
  <div class="space-y-4">
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
      <Select v-model="sessionModel">
        <SelectTrigger class="w-full">
          <SelectValue :placeholder="t('bots.schedule.execution.sessionPlaceholder')" />
        </SelectTrigger>
        <SelectContent>
          <SelectItem
            v-for="session in sessions"
            :key="session.id"
            :value="session.id ?? ''"
          >
            {{ sessionLabel(session) }}
          </SelectItem>
        </SelectContent>
      </Select>
      <p
        v-if="selectedSession"
        class="text-caption text-muted-foreground"
      >
        {{ selectedSessionSummary }}
      </p>
    </FieldStack>

    <FieldStack :label="t('bots.schedule.execution.model')">
      <!-- Existing-session mode inherits the runtime; only the matching model
           column is offered. New-session mode picks the runtime here. -->
      <Select
        v-if="form.runTarget === 'new_session'"
        v-model="runtimeModel"
      >
        <SelectTrigger class="w-full">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="default">
            {{ t('bots.schedule.execution.botDefault') }}
          </SelectItem>
          <SelectGroup v-if="chatModels.length > 0">
            <SelectLabel>{{ t('bots.schedule.execution.models') }}</SelectLabel>
            <SelectItem
              v-for="model in chatModels"
              :key="model.id"
              :value="`model:${model.id}`"
            >
              {{ model.name || model.model_id }}
            </SelectItem>
          </SelectGroup>
          <SelectGroup v-if="enabledAgents.length > 0">
            <SelectLabel>{{ t('bots.schedule.execution.acpAgents') }}</SelectLabel>
            <SelectItem
              v-for="agent in enabledAgents"
              :key="agent.id"
              :value="`acp:${agent.id}`"
            >
              {{ agent.display_name || agent.id }}
            </SelectItem>
          </SelectGroup>
        </SelectContent>
      </Select>
      <Select
        v-else-if="!selectedSessionIsACP"
        v-model="nativeModelModel"
      >
        <SelectTrigger class="w-full">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="default">
            {{ t('bots.schedule.execution.sessionDefault') }}
          </SelectItem>
          <SelectItem
            v-for="model in chatModels"
            :key="model.id"
            :value="model.id ?? ''"
          >
            {{ model.name || model.model_id }}
          </SelectItem>
        </SelectContent>
      </Select>
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
        <Select
          v-else-if="acpCatalog"
          v-model="acpModelModel"
        >
          <SelectTrigger class="w-full">
            <SelectValue :placeholder="t('bots.schedule.execution.agentDefaultModel')" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="default">
              {{ t('bots.schedule.execution.agentDefaultModel') }}
            </SelectItem>
            <SelectItem
              v-for="model in acpCatalog.models"
              :key="model.id"
              :value="model.id ?? ''"
            >
              {{ model.name || model.id }}
            </SelectItem>
          </SelectContent>
        </Select>
      </template>
    </FieldStack>

    <FieldStack
      v-if="effortOptions.length > 0"
      :label="t('bots.schedule.execution.reasoning')"
    >
      <Select v-model="effortModel">
        <SelectTrigger class="w-full">
          <SelectValue :placeholder="t('bots.schedule.execution.defaultEffort')" />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="default">
            {{ t('bots.schedule.execution.defaultEffort') }}
          </SelectItem>
          <SelectItem
            v-for="effort in effortOptions"
            :key="effort.value"
            :value="effort.value"
          >
            {{ effort.label }}
          </SelectItem>
        </SelectContent>
      </Select>
    </FieldStack>

    <FieldStack
      v-if="form.runTarget === 'new_session' && workdirs.length > 0"
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
  </div>
</template>

<script setup lang="ts">
/* eslint-disable vue/no-mutating-props -- parent-owned reactive form, same
   contract as the settings-*-card children. */
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  FieldStack,
  InlineLoadingRow,
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectTrigger,
  SelectValue,
} from '@felinic/ui'
import {
  deleteBotsByBotIdAcpRuntimesByRuntimeId,
  getAcpProfiles,
  getBotsByBotIdSessions,
  getBotsByBotIdWorkdirs,
  getBotsById,
  getModels,
  postBotsByBotIdAcpRuntimes,
} from '@memohai/sdk'
import type {
  AcpclientModelInfo,
  AcpclientReasoningEffortInfo,
  AcpprofilePublicProfile,
  ModelsGetResponse,
  SessionSession,
  WorkdirWorkdir,
} from '@memohai/sdk'
import { resolveApiErrorMessage } from '@/utils/api-error'
import { isACPAgentEnabled, normalizeACPAgentID } from '@/utils/acp'
import { EFFORT_LABELS, resolveEffortLevels, resolveThinkingMode } from './reasoning-effort'

// ScheduleExecutionForm is the editor-owned execution state this component
// mutates in place; the editor serializes it into the API execution block.
export interface ScheduleExecutionForm {
  runTarget: 'new_session' | 'existing_session'
  targetSessionId: string
  runtimeType: '' | 'acp_agent'
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

const sessions = ref<SessionSession[]>([])
const models = ref<ModelsGetResponse[]>([])
const workdirs = ref<WorkdirWorkdir[]>([])
const acpProfiles = ref<AcpprofilePublicProfile[]>([])
const botMetadata = ref<Record<string, unknown> | undefined>(undefined)

interface ACPCatalog {
  agentId: string
  models: AcpclientModelInfo[]
  efforts: AcpclientReasoningEffortInfo[]
}
const acpCatalog = ref<ACPCatalog | null>(null)
const acpCatalogLoading = ref(false)
const acpCatalogError = ref<string | null>(null)

const chatModels = computed(() =>
  models.value.filter((m) => m.type === 'chat' && m.enable !== false),
)

const enabledAgents = computed(() =>
  acpProfiles.value.filter((profile) => isACPAgentEnabled(botMetadata.value, profile.id)),
)

const selectableWorkdirs = computed(() =>
  // The ACP runtime lives in the native workspace; remote workdirs cannot
  // host it (same policy as session creation).
  props.form.runtimeType === 'acp_agent'
    ? workdirs.value.filter((wd) => wd.target_kind !== 'remote')
    : workdirs.value,
)

const selectedSession = computed(() =>
  sessions.value.find((s) => s.id === props.form.targetSessionId) ?? null,
)

const selectedSessionIsACP = computed(() => selectedSession.value?.runtime_type === 'acp_agent')

const selectedSessionAgentID = computed(() => {
  if (!selectedSessionIsACP.value) return ''
  const meta = selectedSession.value?.metadata as Record<string, unknown> | undefined
  return normalizeACPAgentID(meta?.acp_agent_id)
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
    if (next === 'existing_session') void fetchSessions()
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

// runtimeModel folds runtime + model + agent into one picker for new
// sessions: 'default' | 'model:<uuid>' | 'acp:<agent_id>'.
const runtimeModel = computed({
  get: () => {
    if (props.form.runtimeType === 'acp_agent' && props.form.acpAgentId) return `acp:${props.form.acpAgentId}`
    if (props.form.modelId) return `model:${props.form.modelId}`
    return 'default'
  },
  set: (value: string) => {
    props.form.modelId = ''
    props.form.acpModelId = ''
    props.form.reasoningEffort = ''
    if (value.startsWith('acp:')) {
      props.form.runtimeType = 'acp_agent'
      props.form.acpAgentId = value.slice('acp:'.length)
      return
    }
    props.form.runtimeType = ''
    props.form.acpAgentId = ''
    if (value.startsWith('model:')) props.form.modelId = value.slice('model:'.length)
  },
})

const nativeModelModel = computed({
  get: () => props.form.modelId || 'default',
  set: (value: string) => {
    props.form.modelId = value === 'default' ? '' : value
    props.form.reasoningEffort = ''
  },
})

const acpModelModel = computed({
  get: () => props.form.acpModelId || 'default',
  set: (value: string) => {
    props.form.acpModelId = value === 'default' ? '' : value
  },
})

const effortModel = computed({
  get: () => props.form.reasoningEffort || 'default',
  set: (value: string) => {
    props.form.reasoningEffort = value === 'default' ? '' : value
  },
})

const workdirModel = computed({
  get: () => props.form.workdirId || 'default',
  set: (value: string) => {
    props.form.workdirId = value === 'default' ? '' : value
  },
})

const effortOptions = computed<{ value: string; label: string }[]>(() => {
  if (acpAgentInPlay.value) {
    if (!acpCatalog.value) return []
    return acpCatalog.value.efforts
      .filter((effort) => effort.id)
      .map((effort) => ({ value: effort.id ?? '', label: effort.name || effort.id || '' }))
  }
  // Native efforts follow the selected model's advertised tiers; without an
  // explicit model the bot default applies and its own effort rides along,
  // so no selector is shown.
  if (!props.form.modelId) return []
  const model = chatModels.value.find((m) => m.id === props.form.modelId)
  if (!model) return []
  const config = model.config as Parameters<typeof resolveThinkingMode>[0]
  if (resolveThinkingMode(config) === 'none') return []
  return resolveEffortLevels(config).map((effort) => ({
    value: effort,
    label: EFFORT_LABELS[effort] ? t(EFFORT_LABELS[effort]) : effort,
  }))
})

function sessionLabel(session: SessionSession): string {
  const title = session.title?.trim()
  if (title) return title
  return t('bots.schedule.execution.untitledSession', { id: (session.id ?? '').slice(0, 8) })
}

async function fetchSessions() {
  if (sessions.value.length > 0 || !props.botId) return
  try {
    const { data } = await getBotsByBotIdSessions({
      path: { bot_id: props.botId },
      query: { limit: 50 },
      throwOnError: true,
    })
    sessions.value = data.items ?? []
  } catch {
    sessions.value = []
  }
}

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
      body: { acp_agent_id: agentID },
      throwOnError: true,
    })
    acpCatalog.value = {
      agentId: agentID,
      models: data.models?.available_models ?? [],
      efforts: data.reasoning?.available_efforts ?? [],
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
  if (agentID && acpAgentInPlay.value) void loadACPCatalog(agentID)
}, { immediate: true })

onMounted(async () => {
  if (props.form.runTarget === 'existing_session') void fetchSessions()
  await Promise.all([
    (async () => {
      try {
        const { data } = await getModels({ throwOnError: true })
        models.value = data ?? []
      } catch { models.value = [] }
    })(),
    (async () => {
      try {
        const { data } = await getBotsByBotIdWorkdirs({ path: { bot_id: props.botId }, throwOnError: true })
        workdirs.value = data.workdirs ?? []
      } catch { workdirs.value = [] }
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
