<!-- eslint-disable vue/no-mutating-props -->
<template>
  <div class="space-y-8">
    <SettingsSection :title="isGenericACP ? t('bots.settings.acpLaunchSection') : undefined">
      <div class="space-y-5 p-4">
        <SegmentedControl
          v-if="!isGenericACP"
          :model-value="agent.setup_mode"
          :items="setupModeItems"
          :aria-label="$t('bots.settings.acpSetupMode')"
          class="w-full sm:w-fit"
          @update:model-value="(mode) => setSetupMode(String(mode))"
        />

        <!-- 空的字段栈会白占一格 space-y —— 有字段才渲染。 -->
        <AcpManagedFields
          v-if="agent.setup_mode !== 'self' && visibleManagedFields.length > 0"
          :profile="profile"
          :managed="agent.managed"
          :fields="visibleManagedFields"
          settings-mode
          @field-commit="commitForm"
        />

        <p
          v-else-if="agent.setup_mode === 'self'"
          class="break-words text-sm text-muted-foreground"
        >
          {{ t('bots.settings.acpSelfModeHint') }}
        </p>
      </div>
    </SettingsSection>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  SegmentedControl,
  SettingsSection,
} from '@felinic/ui'
import {
  type AcpprofilePublicProfile,
} from '@memohai/sdk'
import { useAcpSetupModeItems } from '@/composables/useAcpSetupModeItems'
import {
  ensureACPAgentForm,
  findMissingRequiredManagedField,
  isACPAgent,
  type ACPAgentForm,
  type ACPForm,
} from '@/utils/acp'
import { filterSettingsVisibleManagedFields } from '@/utils/acp/setup-fields'
import AcpManagedFields from './acp-managed-fields.vue'

const props = defineProps<{
  botId: string
  profile: AcpprofilePublicProfile
  form: ACPForm
}>()

const emit = defineEmits<{
  commit: []
}>()

const { t } = useI18n()
const { setupModeItems } = useAcpSetupModeItems(() => props.profile)

const agent = computed<ACPAgentForm>(() => ensureACPAgentForm(props.form, props.profile))
const isGenericACP = computed(() => isACPAgent(props.profile.id))

const visibleManagedFields = computed(() =>
  filterSettingsVisibleManagedFields(props.profile, agent.value.managed, agent.value.setup_mode),
)

function commitForm() {
  if (agent.value.enabled && findMissingRequiredManagedField(props.profile, agent.value.managed, agent.value.setup_mode)) {
    return
  }
  emit('commit')
}

function setSetupMode(mode: string) {
  agent.value.setup_mode = mode
  commitForm()
}
</script>
