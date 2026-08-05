<script setup lang="ts">
// Creation-time setup panel for one hosted ACP agent: setup-mode chooser +
// managed fields (api_key mode) / deferral hints (oauth, self). Shared by the
// new-bot page and onboarding — it owns only the *pre-create* slice, so the
// interactive OAuth flows (device code, code exchange) deliberately stay out:
// a bot must exist before authorization can run.
//
// State lives inside the panel; parents pull a snapshot via the exposed
// `selection()` at submit and gate on `missingRequiredField()` — the same
// pull-at-submit contract the onboarding step used before the extraction.
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  FieldStack,
  FormStack,
  Input,
  SegmentedControl,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  type SegmentedItem,
} from '@felinic/ui'
import type { AcpprofileManagedField, AcpprofilePublicProfile } from '@memohai/sdk'
import {
  HERMES_CUSTOM_MODEL_VALUE,
  HERMES_PROVIDER_PRESETS,
  defaultSetupMode,
  ensureHermesManagedDefaults,
  findMissingRequiredManagedField,
  hermesAPIKeyPlaceholder,
  hermesDefaultModel,
  hermesModelPresets,
  hermesModelSelectValue,
  hermesProviderValue,
  isClaudeCodeAgent,
  isCodexAgent,
  isHermesCustomProvider,
  isHermesPresetModel,
  normalizeACPAgentID,
} from '@/utils/acp'

export interface AcpSetupSelection {
  agentId: string
  setupMode: string
  managed: Record<string, string>
}

const props = defineProps<{
  profile: AcpprofilePublicProfile
  // Where OAuth authorization will happen, in the caller's words (it differs
  // per surface: onboarding runs a post-create step, the new-bot page defers
  // to bot settings). Caller i18n, no fallback — same contract as ConfirmPopover.
  oauthHint: string
}>()

// Set by the parent after a rejected submit; cleared on any edit. v-model so
// the panel renders the message in one consistent spot instead of every
// surface hand-rolling its own error line.
const errorMessage = defineModel<string>('errorMessage', { default: '' })

const { t } = useI18n()

const setupMode = ref('api_key')
const managed = reactive<Record<string, string>>({})

const isHermes = computed(() => normalizeACPAgentID(props.profile.id) === 'hermes')

// (Re)initialize whenever a different agent is picked: seed one empty slot per
// managed field and prefer the profile's default setup mode.
watch(() => props.profile, (profile) => {
  for (const key of Object.keys(managed)) delete managed[key]
  for (const field of profile.managed_fields ?? []) {
    const id = normalizeACPAgentID(field.id)
    if (id) managed[id] = ''
  }
  const modes = setupModes()
  const preferred = defaultSetupMode(profile)
  setupMode.value = modes.includes(preferred) ? preferred : (modes[0] ?? defaultSetupMode(profile))
  if (isHermes.value && setupMode.value === 'api_key') {
    ensureHermesManagedDefaults(managed)
  }
  errorMessage.value = ''
}, { immediate: true })

function setupModes(): string[] {
  const modes = (props.profile.setup_modes ?? []).filter(Boolean)
  return modes.length > 0 ? modes : ['api_key']
}

function setupModeLabel(mode: string): string {
  if (mode === 'api_key') return t('bots.settings.acpSetupApiKey')
  if (mode === 'oauth') {
    if (isCodexAgent(props.profile.id)) return t('bots.settings.acpSetupChatGPT')
    if (isClaudeCodeAgent(props.profile.id)) return t('bots.settings.acpSetupClaude')
    return t('bots.settings.acpSetupOAuth')
  }
  if (mode === 'self') return t('bots.settings.acpSetupSelf')
  return mode
}

const setupModeItems = computed<SegmentedItem<string>[]>(() =>
  setupModes().map(mode => ({ value: mode, label: setupModeLabel(mode) })),
)

function setSetupMode(mode: string) {
  setupMode.value = mode
  if (isHermes.value && mode === 'api_key') {
    ensureHermesManagedDefaults(managed)
  }
  errorMessage.value = ''
}

// Creation shows managed fields for api_key mode only — oauth defers to a
// later authorization step, self reuses the workspace's own config, so neither
// has anything to fill in here.
const visibleManagedFields = computed<AcpprofileManagedField[]>(() => {
  if (setupMode.value !== 'api_key') return []
  return (props.profile.managed_fields ?? []).filter((field) => {
    const id = normalizeACPAgentID(field.id)
    if (!id || id === 'provider_id' || id === 'oauth_token') return false
    if (isHermes.value && id === 'base_url') return isHermesCustomProvider(hermesProvider.value)
    return true
  })
})

// Hermes provider/model fields render their own preset selects and never carry
// help text; every other managed field surfaces its schema-provided help.
function managedFieldHelp(field: AcpprofileManagedField): string {
  const id = normalizeACPAgentID(field.id)
  if (isHermes.value && (id === 'provider' || id === 'model')) return ''
  return field.help || ''
}

function inputType(type: string | undefined): string {
  if (type === 'password') return 'password'
  if (type === 'url') return 'url'
  return 'text'
}

function managedPlaceholder(field: AcpprofileManagedField): string | undefined {
  if (isHermes.value && normalizeACPAgentID(field.id) === 'api_key') {
    return hermesAPIKeyPlaceholder(hermesProvider.value, field.placeholder)
  }
  return field.placeholder
}

function setManagedField(fieldID: string | undefined, value: string) {
  const id = normalizeACPAgentID(fieldID)
  if (!id) return
  managed[id] = value
  errorMessage.value = ''
}

// --- Hermes preset handling (provider → model presets, custom endpoint) ---

const hermesProvider = computed(() => hermesProviderValue(managed.provider))

function isHermesProviderField(field: AcpprofileManagedField): boolean {
  return isHermes.value && normalizeACPAgentID(field.id) === 'provider'
}

function isHermesModelField(field: AcpprofileManagedField): boolean {
  return isHermes.value && normalizeACPAgentID(field.id) === 'model'
}

function hermesModelOptions() {
  return hermesModelPresets(hermesProvider.value)
}

function hermesModelSelect(): string {
  return hermesModelSelectValue(hermesProvider.value, managed.model)
}

const hermesUsingCustomModel = computed(() => hermesModelSelect() === HERMES_CUSTOM_MODEL_VALUE)

function setHermesProvider(value: string) {
  const provider = hermesProviderValue(value)
  managed.provider = provider
  managed.model = hermesDefaultModel(provider)
  if (!isHermesCustomProvider(provider)) managed.base_url = ''
  errorMessage.value = ''
}

function setHermesModel(value: string) {
  if (value === HERMES_CUSTOM_MODEL_VALUE) {
    if (isHermesPresetModel(hermesProvider.value, managed.model)) {
      managed.model = ''
    }
  } else {
    managed.model = value
  }
  errorMessage.value = ''
}

const selfModeHint = computed(() => isHermes.value
  ? t('bots.settings.acpHermesSelfModeHint')
  : t('bots.settings.acpSelfModeHint'))

// selection snapshots the parent's create payload: api_key collects only the
// visible fields (trimmed, non-empty); other modes carry no managed values.
function selection(): AcpSetupSelection {
  const managedSnapshot: Record<string, string> = {}
  if (setupMode.value === 'api_key') {
    for (const field of visibleManagedFields.value) {
      const id = normalizeACPAgentID(field.id)
      const value = (managed[id ?? ''] ?? '').trim()
      if (value) managedSnapshot[id] = value
    }
  }
  return {
    agentId: normalizeACPAgentID(props.profile.id),
    setupMode: setupMode.value,
    managed: managedSnapshot,
  }
}

// missingRequiredField is the submit-time gate: the first required api_key
// field left empty, or null when the selection is submittable.
function missingRequiredField(): AcpprofileManagedField | null {
  if (setupMode.value !== 'api_key') return null
  return findMissingRequiredManagedField(props.profile, managed, setupMode.value)
}

defineExpose({ selection, missingRequiredField })
</script>

<template>
  <FormStack>
    <FieldStack :label="t('bots.settings.acpSetupMode')">
      <SegmentedControl
        :model-value="setupMode"
        :items="setupModeItems"
        :aria-label="t('bots.settings.acpSetupMode')"
        class="w-full sm:w-fit"
        @update:model-value="setSetupMode"
      />
    </FieldStack>

    <template v-if="setupMode === 'api_key'">
      <FieldStack
        v-for="field in visibleManagedFields"
        :key="field.id"
        :label="field.label || field.id"
        :help="managedFieldHelp(field)"
      >
        <Select
          v-if="isHermesProviderField(field)"
          :model-value="hermesProvider"
          @update:model-value="(value) => setHermesProvider(String(value))"
        >
          <SelectTrigger class="w-full">
            <SelectValue :placeholder="t('bots.settings.acpHermesProviderPlaceholder')" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem
              v-for="provider in HERMES_PROVIDER_PRESETS"
              :key="provider.value"
              :value="provider.value"
            >
              {{ t(provider.labelKey) }}
            </SelectItem>
          </SelectContent>
        </Select>
        <template v-else-if="isHermesModelField(field)">
          <Select
            :model-value="hermesModelSelect()"
            @update:model-value="(value) => setHermesModel(String(value))"
          >
            <SelectTrigger class="w-full">
              <SelectValue :placeholder="t('bots.settings.acpHermesModelPlaceholder')" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem
                v-for="model in hermesModelOptions()"
                :key="model.value"
                :value="model.value"
              >
                {{ model.label }}
              </SelectItem>
              <SelectItem :value="HERMES_CUSTOM_MODEL_VALUE">
                {{ t('bots.settings.acpHermesCustomModel') }}
              </SelectItem>
            </SelectContent>
          </Select>
          <Input
            v-if="hermesUsingCustomModel"
            class="mt-2"
            :model-value="managed.model || ''"
            autocomplete="off"
            autocapitalize="off"
            autocorrect="off"
            spellcheck="false"
            :placeholder="t('bots.settings.acpHermesCustomModelPlaceholder')"
            @update:model-value="(val) => setManagedField(field.id, String(val ?? ''))"
          />
        </template>
        <Input
          v-else
          :model-value="managed[field.id || ''] || ''"
          :type="inputType(field.type)"
          autocomplete="off"
          autocapitalize="off"
          autocorrect="off"
          spellcheck="false"
          :placeholder="managedPlaceholder(field)"
          @update:model-value="(val) => setManagedField(field.id, String(val ?? ''))"
        />
      </FieldStack>
    </template>

    <p
      v-else-if="setupMode === 'oauth'"
      class="text-sm text-muted-foreground"
    >
      {{ oauthHint }}
    </p>
    <p
      v-else-if="setupMode === 'self'"
      class="text-sm text-muted-foreground"
    >
      {{ selfModeHint }}
    </p>

    <p
      v-if="errorMessage"
      class="text-xs text-destructive"
    >
      {{ errorMessage }}
    </p>
  </FormStack>
</template>
