<script setup lang="ts">
/* eslint-disable vue/no-mutating-props -- Parents pass a reactive managed map; field edits mutate in place like settings-acp-detail. */
// Shared managed-field stack for ACP setup (create + settings). Parents own
// setup_mode; this component only renders the schema-driven fields.
import { useI18n } from 'vue-i18n'
import {
  FieldStack,
  FormStack,
  Input,
  Textarea,
} from '@felinic/ui'
import type { AcpprofileManagedField, AcpprofilePublicProfile } from '@memohai/sdk'
import { normalizeACPAgentID } from '@/utils/acp'
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

function setManagedField(fieldID: string | undefined, value: string) {
  const id = normalizeACPAgentID(fieldID)
  if (!id) return
  props.managed[id] = value
  emit('fieldChange')
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
      <Textarea
        v-if="field.type === 'textarea'"
        :model-value="managed[field.id || ''] || ''"
        :name="settingsMode ? acpManagedFieldName(profile, field) : undefined"
        rows="4"
        autocomplete="off"
        autocapitalize="off"
        autocorrect="off"
        spellcheck="false"
        :placeholder="acpManagedPlaceholder(profile, field)"
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
        :placeholder="acpManagedPlaceholder(profile, field)"
        @update:model-value="(val) => handleFieldInput(field, String(val ?? ''))"
        @change="handleFieldCommit"
      />
    </FieldStack>
  </FormStack>
</template>
