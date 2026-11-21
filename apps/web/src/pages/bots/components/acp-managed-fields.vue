<script setup lang="ts">
import { normalizeAgentID } from '@/utils/external-agent'
/* eslint-disable vue/no-mutating-props -- The parent passes a reactive managed map; field edits mutate it in place. */
// Managed-field stack of an ACP agent's settings, rendered from its profile.
import { useI18n } from 'vue-i18n'
import {
  FieldStack,
  FormStack,
  Input,
  Textarea,
} from '@felinic/ui'
import type { AcpprofileManagedField, AcpprofilePublicProfile } from '@memohai/sdk'
import {
  acpInputType,
  acpManagedFieldAutocomplete,
  acpManagedFieldHelp,
  acpManagedFieldLabel,
  acpManagedFieldName,
  acpManagedPlaceholder,
} from '@/utils/acp/setup-fields'

const props = defineProps<{
  profile: AcpprofilePublicProfile
  managed: Record<string, string>
  fields: AcpprofileManagedField[]
}>()

const emit = defineEmits<{
  fieldCommit: []
}>()

const { t } = useI18n()

function handleFieldInput(field: AcpprofileManagedField, value: string) {
  const id = normalizeAgentID(field.id)
  if (id) props.managed[id] = value
}
</script>

<template>
  <FormStack>
    <FieldStack
      v-for="field in fields"
      :key="field.id"
      :label="acpManagedFieldLabel(profile, field, t)"
      :help="acpManagedFieldHelp(profile, field)"
      gap="card"
    >
      <Textarea
        v-if="field.type === 'textarea'"
        :model-value="managed[field.id || ''] || ''"
        :name="acpManagedFieldName(profile, field)"
        rows="4"
        autocomplete="off"
        autocapitalize="off"
        autocorrect="off"
        spellcheck="false"
        :placeholder="acpManagedPlaceholder(profile, field)"
        @update:model-value="(val) => handleFieldInput(field, String(val ?? ''))"
        @change="emit('fieldCommit')"
      />
      <Input
        v-else
        :model-value="managed[field.id || ''] || ''"
        :type="acpInputType(field.type)"
        :name="acpManagedFieldName(profile, field)"
        :autocomplete="acpManagedFieldAutocomplete(field)"
        autocapitalize="off"
        autocorrect="off"
        spellcheck="false"
        :placeholder="acpManagedPlaceholder(profile, field)"
        @update:model-value="(val) => handleFieldInput(field, String(val ?? ''))"
        @change="emit('fieldCommit')"
      />
    </FieldStack>
  </FormStack>
</template>
