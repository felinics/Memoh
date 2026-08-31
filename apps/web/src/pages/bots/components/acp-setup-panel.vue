<script setup lang="ts">
// Creation-time setup panel for one hosted ACP agent: setup-mode chooser +
// managed fields (api_key mode) / deferral hints (oauth, self). Shared by the
// new-bot page and onboarding — it owns only the *pre-create* slice, so the
// interactive OAuth flows (device code, code exchange) deliberately stay out:
// a bot must exist before authorization can run.
//
// Managed fields render via acp-managed-fields.vue (shared with settings).
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { FieldStack, FormStack, SegmentedControl } from '@felinic/ui'
import type { AcpprofileManagedField, AcpprofilePublicProfile } from '@memohai/sdk'
import { useAcpSetupModeItems } from '@/composables/useAcpSetupModeItems'
import {
  defaultSetupMode,
  findMissingRequiredManagedField,
  normalizeACPAgentID,
} from '@/utils/acp'
import { filterCreateVisibleManagedFields } from '@/utils/acp/setup-fields'
import AcpManagedFields from './acp-managed-fields.vue'

export interface AcpSetupSelection {
  agentId: string
  setupMode: string
  managed: Record<string, string>
}

const props = withDefaults(defineProps<{
  profile: AcpprofilePublicProfile
  oauthHint: string
  fieldGap?: 'card' | 'bare'
}>(), {
  fieldGap: 'card',
})

const errorMessage = defineModel<string>('errorMessage', { default: '' })

const { t } = useI18n()
const { setupModeItems, setupModes } = useAcpSetupModeItems(() => props.profile)

const setupMode = ref('api_key')
const managed = reactive<Record<string, string>>({})

const visibleManagedFields = computed(() =>
  filterCreateVisibleManagedFields(props.profile, managed, setupMode.value),
)

const selfModeHint = computed(() => t('bots.settings.acpSelfModeHint'))

watch(() => props.profile, (profile) => {
  for (const key of Object.keys(managed)) delete managed[key]
  for (const field of profile.managed_fields ?? []) {
    const id = normalizeACPAgentID(field.id)
    if (id) managed[id] = ''
  }
  const modes = setupModes()
  const preferred = defaultSetupMode(profile)
  setupMode.value = modes.includes(preferred) ? preferred : (modes[0] ?? defaultSetupMode(profile))
  errorMessage.value = ''
}, { immediate: true })

function setSetupMode(mode: string) {
  setupMode.value = mode
  errorMessage.value = ''
}

function clearErrorMessage() {
  errorMessage.value = ''
}

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

function missingRequiredField(): AcpprofileManagedField | null {
  if (setupMode.value !== 'api_key') return null
  return findMissingRequiredManagedField(props.profile, managed, setupMode.value)
}

defineExpose({ selection, missingRequiredField })
</script>

<template>
  <FormStack>
    <FieldStack
      :label="t('bots.settings.acpSetupMode')"
      :gap="fieldGap"
    >
      <SegmentedControl
        :model-value="setupMode"
        :items="setupModeItems"
        :aria-label="t('bots.settings.acpSetupMode')"
        class="w-full sm:w-fit"
        @update:model-value="setSetupMode"
      />
    </FieldStack>

    <AcpManagedFields
      v-if="setupMode === 'api_key'"
      :profile="profile"
      :managed="managed"
      :fields="visibleManagedFields"
      :field-gap="fieldGap"
      @field-change="clearErrorMessage"
    />

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
