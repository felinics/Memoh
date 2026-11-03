<template>
  <div class="space-y-8">
    <SettingsSection :title="t('bots.settings.acpLaunchSection')">
      <div class="space-y-5 p-4">
        <AcpManagedFields
          v-if="(profile.managed_fields ?? []).length > 0"
          :profile="profile"
          :managed="setup.managed"
          :fields="profile.managed_fields ?? []"
          @field-commit="emit('commit')"
        />
      </div>
    </SettingsSection>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { SettingsSection } from '@felinic/ui'
import type { AcpprofilePublicProfile } from '@memohai/sdk'
import type { ACPAgentForm } from '@/utils/acp'
import AcpManagedFields from './acp-managed-fields.vue'

defineProps<{
  botId: string
  profile: AcpprofilePublicProfile
  setup: ACPAgentForm
}>()

const emit = defineEmits<{
  commit: []
}>()

const { t } = useI18n()
</script>
