<script setup lang="ts">
import { watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button, DeviceCodePanel, LabelSwap, SettingsRow, SettingsSection, Spinner } from '@felinic/ui'
import { KeyRound } from 'lucide-vue-next'
import { useCodexDeviceLogin } from '@/composables/useCodexDeviceLogin'

const props = defineProps<{ botId: string, agentId: string }>()
const emit = defineEmits<{ status: [state: { authorized: boolean, busy: boolean }] }>()
const { t } = useI18n()
const {
  authorized, loadingStatus, authorizing, busy, error, deviceLogin, devicePending,
  loadStatus, authorizeCodex, cancelCodex,
} = useCodexDeviceLogin(() => props.botId, () => props.agentId)

watch([authorized, busy, loadingStatus], () => {
  emit('status', { authorized: authorized.value, busy: busy.value || loadingStatus.value })
}, { immediate: true })
watch([() => props.botId, () => props.agentId], () => { void loadStatus() }, { immediate: true })
</script>

<template>
  <SettingsSection>
    <SettingsRow
      :label="t('bots.agent.chatgptAccount')"
      :description="authorized ? t('bots.agent.chatgptAccountConnectedDescription') : t('bots.agent.authChatGPTDescription')"
    >
      <Button
        v-if="!authorized"
        type="button"
        variant="outline"
        size="sm"
        class="shrink-0"
        :disabled="!agentId || loadingStatus || authorizing"
        :loading="authorizing"
        loading-mode="manual"
        @click="devicePending ? cancelCodex() : authorizeCodex()"
      >
        <LabelSwap :active="authorizing ? 'connecting' : devicePending ? 'cancel' : 'connect'">
          <template #connect>
            <KeyRound />
            {{ t('provider.oauth.connect') }}
          </template>
          <template #connecting>
            <Spinner />
            {{ t('provider.oauth.connecting') }}
          </template>
          <template #cancel>
            {{ t('common.cancel') }}
          </template>
        </LabelSwap>
      </Button>
    </SettingsRow>
    <div
      v-if="devicePending && deviceLogin"
      class="mx-4 border-b border-border py-6 last:border-b-0"
    >
      <DeviceCodePanel
        :code="deviceLogin.user_code"
        :verification-uri="deviceLogin.verification_url"
        :hint="t('bots.agent.codexDeviceHint')"
        :retry-loading="authorizing"
        :copy-and-open-label="t('deviceCode.copyAndOpen')"
        :retry-label="t('deviceCode.retry')"
        :expired-label="t('deviceCode.codeExpired')"
        :expires-in-label="(time: string) => t('deviceCode.expiresIn', { time })"
        :copy-failed-message="t('deviceCode.copyFailed')"
        @retry="authorizeCodex"
      />
    </div>
    <p
      v-if="error"
      class="px-4 text-sm text-destructive"
    >
      {{ error }}
    </p>
  </SettingsSection>
</template>
