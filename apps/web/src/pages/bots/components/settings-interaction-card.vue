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
                :is="botAgentIcon(selectedAgent, true)"
                v-else-if="selectedAgent"
                class="size-4 shrink-0"
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
            v-for="agent in selectableAgents"
            :key="agent.id"
            :value="agentOptionValue(agent.id)"
          >
            <div class="flex min-w-0 items-center gap-2">
              <component
                :is="botAgentIcon(agent, true)"
                class="size-4 shrink-0"
              />
              <span class="truncate">{{ botAgentName(agent) }}</span>
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
import type { AcpprofilePublicProfile, BotagentsBotAgent, SettingsSettings, ModelsGetResponse, ProvidersGetResponse } from '@memohai/sdk'
import { ACP_DEFAULT_PROJECT_MODE, ACP_DEFAULT_PROJECT_PATH, findMissingRequiredManagedField, isACPAgentEnabled, normalizeACPAgentID, readACPAgentConfig } from '@/utils/acp'
import { BOT_AGENT_RUNTIME_CLAUDE_CODE, BOT_AGENT_RUNTIME_CODEX, botAgentIcon, botAgentName, botAgentProvider, isDirectBotAgentConfigured, normalizeBotAgentRuntime } from '@/utils/bot-agent'

type InteractionSettingsForm = SettingsSettings & {
  chat_runtime: string
  chat_acp_agent_id: string
  chat_acp_project_path: string
  chat_acp_project_mode: string
  default_bot_agent_id: string
}

const props = defineProps<{
  form: InteractionSettingsForm
  models: ModelsGetResponse[]
  providers: ProvidersGetResponse[]
  botAgents: BotagentsBotAgent[]
  botMetadata?: Record<string, unknown>
  acpProfiles: AcpprofilePublicProfile[]
}>()

const { t } = useI18n()

const MEMOH_AGENT_VALUE = 'memoh'
const BOT_AGENT_VALUE_PREFIX = 'agent:'

function isAgentConfigured(agent: BotagentsBotAgent): boolean {
  const directConfigured = isDirectBotAgentConfigured(agent)
  if (directConfigured !== null) return directConfigured
  const provider = botAgentProvider(agent)
  const profile = props.acpProfiles.find(item => normalizeACPAgentID(item.id) === provider)
  if (!profile || !isACPAgentEnabled(props.botMetadata, provider)) return false
  const config = readACPAgentConfig(props.botMetadata, provider)
  return !config.setupModeSet || findMissingRequiredManagedField(profile, config.managed, config.setupMode) === null
}

const selectableAgents = computed(() => props.botAgents.filter(agent =>
  agent.enabled !== false && !!agent.id && isAgentConfigured(agent),
))

const defaultBotAgentID = computed(() => props.form.default_bot_agent_id?.trim() ?? '')
const selectedAgent = computed(() => props.botAgents.find(agent => agent.id === defaultBotAgentID.value))
const defaultAgentValue = computed(() =>
  defaultBotAgentID.value
    ? agentOptionValue(defaultBotAgentID.value)
    : MEMOH_AGENT_VALUE,
)
const selectedAgentUnavailable = computed(() =>
  !!defaultBotAgentID.value && (!selectedAgent.value || selectedAgent.value.enabled === false || !isAgentConfigured(selectedAgent.value)),
)
const defaultAgentDescription = computed(() =>
  selectedAgentUnavailable.value
    ? t('bots.settings.defaultAgentUnavailableDescription')
    : t('bots.settings.defaultAgentDescription'),
)
const selectedAgentLabel = computed(() => {
  if (!defaultBotAgentID.value) return t('chat.agentMemoh')
  if (selectedAgent.value) return botAgentName(selectedAgent.value)
  return t('bots.settings.defaultAgentUnavailable')
})

function agentOptionValue(agentID: unknown): string {
  return `${BOT_AGENT_VALUE_PREFIX}${typeof agentID === 'string' ? agentID.trim() : ''}`
}

function ensureDefaultACPProject() {
  // eslint-disable-next-line vue/no-mutating-props
  props.form.chat_acp_project_path = props.form.chat_acp_project_path || ACP_DEFAULT_PROJECT_PATH
  // eslint-disable-next-line vue/no-mutating-props
  props.form.chat_acp_project_mode = props.form.chat_acp_project_mode || ACP_DEFAULT_PROJECT_MODE
}

function setDefaultBotAgent(agent: BotagentsBotAgent) {
  // eslint-disable-next-line vue/no-mutating-props
  props.form.default_bot_agent_id = agent.id?.trim() ?? ''
  // eslint-disable-next-line vue/no-mutating-props
  props.form.chat_acp_agent_id = botAgentProvider(agent)
  ensureDefaultACPProject()
}

function setDefaultAgent(value: string) {
  if (value === MEMOH_AGENT_VALUE) {
    // eslint-disable-next-line vue/no-mutating-props
    props.form.default_bot_agent_id = ''
    // eslint-disable-next-line vue/no-mutating-props
    props.form.chat_runtime = 'model'
    // eslint-disable-next-line vue/no-mutating-props
    props.form.chat_acp_agent_id = ''
    return
  }

  if (!value.startsWith(BOT_AGENT_VALUE_PREFIX)) return
  const agentID = value.slice(BOT_AGENT_VALUE_PREFIX.length).trim()
  const agent = selectableAgents.value.find(item => item.id === agentID)
  if (!agent) return

  setDefaultBotAgent(agent)
  const runtime = normalizeBotAgentRuntime(agent.runtime)
  const direct = runtime === BOT_AGENT_RUNTIME_CODEX || runtime === BOT_AGENT_RUNTIME_CLAUDE_CODE
  // eslint-disable-next-line vue/no-mutating-props
  props.form.chat_runtime = direct ? runtime : 'acp_agent'
  if (direct) {
    // A direct default is fully described by its runtime.
    // eslint-disable-next-line vue/no-mutating-props
    props.form.chat_acp_agent_id = ''
  }
}

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
