<script setup lang="ts">
/* eslint-disable vue/no-mutating-props -- Parents pass a reactive managed map; field edits mutate in place like settings-acp-detail. */
// Shared managed-field stack for ACP setup (create + settings). Parents own
// setup_mode / OAuth; this component only renders the schema-driven fields.
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  FieldStack,
  FormStack,
  Input,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  Textarea,
} from '@felinic/ui'
import type { AcpprofileManagedField, AcpprofilePublicProfile } from '@memohai/sdk'
import {
  HERMES_CUSTOM_MODEL_VALUE,
  HERMES_PROVIDER_PRESETS,
  hermesDefaultModel,
  hermesModelPresets,
  hermesModelSelectValue,
  hermesProviderValue,
  isHermesCustomProvider,
  isHermesPresetModel,
  normalizeACPAgentID,
} from '@/utils/acp'
import {
  acpInputType,
  acpManagedFieldAutocomplete,
  acpManagedFieldHelp,
  acpManagedFieldLabel,
  acpManagedFieldName,
  acpManagedPlaceholder,
} from '@/utils/acp/setup-fields'

const props = withDefaults(defineProps<{
  profile: AcpprofilePublicProfile
  managed: Record<string, string>
  fields: AcpprofileManagedField[]
  fieldGap?: 'card' | 'bare'
  /** Settings inputs get stable names and emit fieldCommit on native change. */
  settingsMode?: boolean
}>(), {
  fieldGap: 'card',
  settingsMode: false,
})

const emit = defineEmits<{
  fieldChange: []
  fieldCommit: []
}>()

const { t } = useI18n()

const isHermes = computed(() => normalizeACPAgentID(props.profile.id) === 'hermes')
const hermesProvider = computed(() => hermesProviderValue(props.managed.provider))
const hermesModelOptions = computed(() => hermesModelPresets(hermesProvider.value))
const hermesModelSelect = computed(() => hermesModelSelectValue(hermesProvider.value, props.managed.model))
const hermesUsingCustomModel = computed(() => hermesModelSelect.value === HERMES_CUSTOM_MODEL_VALUE)

function isHermesProviderField(field: AcpprofileManagedField): boolean {
  return isHermes.value && normalizeACPAgentID(field.id) === 'provider'
}

function isHermesModelField(field: AcpprofileManagedField): boolean {
  return isHermes.value && normalizeACPAgentID(field.id) === 'model'
}

function setManagedField(fieldID: string | undefined, value: string) {
  const id = normalizeACPAgentID(fieldID)
  if (!id) return
  props.managed[id] = value
  emit('fieldChange')
}

function setHermesProvider(value: string) {
  const provider = hermesProviderValue(value)
  props.managed.provider = provider
  props.managed.model = hermesDefaultModel(provider)
  if (!isHermesCustomProvider(provider)) props.managed.base_url = ''
  emit('fieldChange')
  if (props.settingsMode) emit('fieldCommit')
}

function setHermesModel(value: string) {
  if (value === HERMES_CUSTOM_MODEL_VALUE) {
    if (isHermesPresetModel(hermesProvider.value, props.managed.model)) {
      props.managed.model = ''
    }
  } else {
    props.managed.model = value
  }
  emit('fieldChange')
  if (props.settingsMode) emit('fieldCommit')
}

function handleFieldInput(field: AcpprofileManagedField, value: string) {
  setManagedField(field.id, value)
}

function handleFieldCommit() {
  emit('fieldCommit')
}
</script>

<template>
  <FormStack>
    <FieldStack
      v-for="field in fields"
      :key="field.id"
      :label="acpManagedFieldLabel(profile, field, t)"
      :help="acpManagedFieldHelp(profile, field, t)"
      :gap="fieldGap"
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
          :model-value="hermesModelSelect"
          @update:model-value="(value) => setHermesModel(String(value))"
        >
          <SelectTrigger class="w-full">
            <SelectValue :placeholder="t('bots.settings.acpHermesModelPlaceholder')" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem
              v-for="model in hermesModelOptions"
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
          :name="settingsMode ? acpManagedFieldName(profile, field) : undefined"
          autocomplete="off"
          autocapitalize="off"
          autocorrect="off"
          spellcheck="false"
          :placeholder="t('bots.settings.acpHermesCustomModelPlaceholder')"
          @update:model-value="(val) => handleFieldInput(field, String(val ?? ''))"
          @change="handleFieldCommit"
        />
      </template>
      <Textarea
        v-else-if="field.type === 'textarea'"
        :model-value="managed[field.id || ''] || ''"
        :name="settingsMode ? acpManagedFieldName(profile, field) : undefined"
        rows="4"
        autocomplete="off"
        autocapitalize="off"
        autocorrect="off"
        spellcheck="false"
        :placeholder="acpManagedPlaceholder(profile, field, hermesProvider)"
        @update:model-value="(val) => handleFieldInput(field, String(val ?? ''))"
        @change="handleFieldCommit"
      />
      <Input
        v-else
        :model-value="managed[field.id || ''] || ''"
        :type="acpInputType(field.type)"
        :name="settingsMode ? acpManagedFieldName(profile, field) : undefined"
        :autocomplete="settingsMode ? acpManagedFieldAutocomplete(field) : 'off'"
        autocapitalize="off"
        autocorrect="off"
        spellcheck="false"
        :placeholder="acpManagedPlaceholder(profile, field, hermesProvider)"
        @update:model-value="(val) => handleFieldInput(field, String(val ?? ''))"
        @change="handleFieldCommit"
      />
    </FieldStack>
  </FormStack>
</template>
