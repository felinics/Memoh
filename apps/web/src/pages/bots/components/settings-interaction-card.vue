<!-- eslint-disable vue/no-mutating-props -->
<template>
  <!-- id is a scroll anchor: the Overview "choose a model" reminder navigates
       here with ?section=interaction, and bot-settings.vue scrolls to it. -->
  <SettingsSection
    id="settings-section-interaction"
    :title="$t('bots.settings.blocks.interaction')"
  >
    <SettingsRow
      :label="$t('bots.settings.defaultAgent')"
      :description="defaultAgentDescription"
      stack="sm"
    >
      <Select
        :model-value="defaultAgentValue"
        @update:model-value="(value) => setDefaultAgent(String(value))"
      >
        <SelectTrigger class="w-full sm:w-56">
          <SelectValue>
            <div class="flex min-w-0 items-center gap-2">
              <img
                v-if="defaultAgentValue === MEMOH_AGENT_VALUE"
                src="/logo.svg"
                alt=""
                class="size-4 shrink-0"
              >
              <component
                :is="acpAgentIcon(selectedACPProfile.id, true)"
                v-else-if="selectedACPProfile"
              />
              <span class="truncate">{{ selectedAgentLabel }}</span>
            </div>
          </SelectValue>
        </SelectTrigger>
        <SelectContent>
          <SelectItem :value="MEMOH_AGENT_VALUE">
            <div class="flex min-w-0 items-center gap-2">
              <img
                src="/logo.svg"
                alt=""
                class="size-4 shrink-0"
              >
              <span class="truncate">{{ $t('chat.agentMemoh') }}</span>
            </div>
          </SelectItem>
          <SelectItem
            v-for="profile in selectableACPProfiles"
            :key="profile.id"
            :value="agentOptionValue(profile.id)"
          >
            <div class="flex min-w-0 items-center gap-2">
              <component :is="acpAgentIcon(profile.id, true)" />
              <span class="truncate">{{ profile.display_name || profile.id }}</span>
            </div>
          </SelectItem>
        </SelectContent>
      </Select>
    </SettingsRow>

    <SettingsRow
      :label="$t('bots.settings.chatModel')"
      :description="$t('bots.settings.chatModelDescription')"
      stack="sm"
    >
      <div class="w-full sm:w-56">
        <ModelSelect
          v-model="form.chat_model_id"
          v-model:reasoning-effort="form.reasoning_effort"
          :models="models"
          :providers="providers"
          model-type="chat"
          :placeholder="$t('bots.settings.chatModelPlaceholder')"
          show-reasoning
        />
      </div>
    </SettingsRow>

    <SettingsRow
      :label="$t('bots.settings.showToolCallsInIM')"
      :description="$t('bots.settings.showToolCallsInIMDescription')"
    >
      <Switch
        :model-value="form.show_tool_calls_in_im"
        @update:model-value="(val) => form.show_tool_calls_in_im = !!val"
      />
    </SettingsRow>
  </SettingsSection>
</template>

<script setup lang="ts">
import { computed, watch } from 'vue'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue, SettingsRow, SettingsSection, Switch } from '@felinic/ui'
import { useI18n } from 'vue-i18n'
import ModelSelect from './model-select.vue'
import { reconcileStoredEffort } from './reasoning-effort'
import type { AcpprofilePublicProfile, SettingsSettings, ModelsGetResponse, ProvidersGetResponse } from '@memohai/sdk'
import { ACP_DEFAULT_PROJECT_MODE, ACP_DEFAULT_PROJECT_PATH, acpAgentIcon, findMissingRequiredManagedField, isACPAgentEnabled, normalizeACPAgentID, readACPAgentConfig } from '@/utils/acp'

type InteractionSettingsForm = SettingsSettings & {
  chat_runtime: string
  chat_acp_agent_id: string
  chat_acp_project_path: string
  chat_acp_project_mode: string
}

const props = defineProps<{
  form: InteractionSettingsForm
  models: ModelsGetResponse[]
  providers: ProvidersGetResponse[]
  acpProfiles: AcpprofilePublicProfile[]
  botMetadata?: Record<string, unknown>
}>()

const { t } = useI18n()

const MEMOH_AGENT_VALUE = 'memoh'
const ACP_AGENT_VALUE_PREFIX = 'acp:'

const selectableACPProfiles = computed(() =>
  props.acpProfiles.filter((profile) => {
    if (!isACPAgentEnabled(props.botMetadata, profile.id)) return false
    const config = readACPAgentConfig(props.botMetadata, profile.id)
    return !config.setupModeSet || !findMissingRequiredManagedField(profile, config.managed, config.setupMode)
  }),
)

const externalAgentEnabled = computed(() => props.form.chat_runtime === 'acp_agent')
const normalizedDefaultAgentID = computed(() => normalizeACPAgentID(props.form.chat_acp_agent_id))
const selectedACPProfile = computed(() =>
  selectableACPProfiles.value.find(profile => normalizeACPAgentID(profile.id) === normalizedDefaultAgentID.value),
)
const defaultAgentValue = computed(() =>
  externalAgentEnabled.value
    ? agentOptionValue(normalizedDefaultAgentID.value)
    : MEMOH_AGENT_VALUE,
)
const selectedACPUnavailable = computed(() =>
  externalAgentEnabled.value && !selectedACPProfile.value,
)
const defaultAgentDescription = computed(() =>
  selectedACPUnavailable.value
    ? t('bots.settings.defaultAgentUnavailableDescription')
    : t('bots.settings.defaultAgentDescription'),
)
const selectedAgentLabel = computed(() => {
  if (!externalAgentEnabled.value) return t('chat.agentMemoh')
  if (selectedACPProfile.value) return selectedACPProfile.value.display_name || selectedACPProfile.value.id
  return t('bots.settings.defaultAgentUnavailable')
})

function agentOptionValue(agentID: unknown): string {
  return `${ACP_AGENT_VALUE_PREFIX}${normalizeACPAgentID(agentID)}`
}

function ensureDefaultACPProject() {
  // eslint-disable-next-line vue/no-mutating-props
  props.form.chat_acp_project_path = props.form.chat_acp_project_path || ACP_DEFAULT_PROJECT_PATH
  // eslint-disable-next-line vue/no-mutating-props
  props.form.chat_acp_project_mode = props.form.chat_acp_project_mode || ACP_DEFAULT_PROJECT_MODE
}

function setDefaultACPAgent(agentID: string) {
  // eslint-disable-next-line vue/no-mutating-props
  props.form.chat_acp_agent_id = normalizeACPAgentID(agentID)
  ensureDefaultACPProject()
}

function setDefaultAgent(value: string) {
  if (value === MEMOH_AGENT_VALUE) {
    // eslint-disable-next-line vue/no-mutating-props
    props.form.chat_runtime = 'model'
    return
  }

  if (!value.startsWith(ACP_AGENT_VALUE_PREFIX)) return
  const agentID = normalizeACPAgentID(value.slice(ACP_AGENT_VALUE_PREFIX.length))
  if (!agentID) return
  const profile = selectableACPProfiles.value.find(item => normalizeACPAgentID(item.id) === agentID)
  if (!profile) return

  setDefaultACPAgent(agentID)
  // eslint-disable-next-line vue/no-mutating-props
  props.form.chat_runtime = 'acp_agent'
}

watch(selectableACPProfiles, (profiles) => {
  if (!externalAgentEnabled.value || profiles.length === 0 || normalizedDefaultAgentID.value || selectedACPProfile.value) return
  const firstAgentID = normalizeACPAgentID(profiles[0]?.id)
  if (firstAgentID) setDefaultACPAgent(firstAgentID)
}, { immediate: true })

const chatModelReasoning = computed(() => {
  if (!props.form.chat_model_id) return undefined
  return props.models.find((m) => m.id === props.form.chat_model_id)?.reasoning
})

// Switching models can strand the stored tier on a model that does not offer it.
// A model with no thinking at all has nothing to migrate to, so it is left alone
// rather than cleared — the stored value is still meaningful if the user
// switches back.
watch(chatModelReasoning, (options) => {
  if (!options?.supported) return
  const current = props.form.reasoning_effort ?? ''
  const next = reconcileStoredEffort(current, options)
  if (next === current) return
  // eslint-disable-next-line vue/no-mutating-props
  props.form.reasoning_effort = next
}, { immediate: true })
</script>
