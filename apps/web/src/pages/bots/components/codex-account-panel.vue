<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { Button, DeviceCodePanel, Dialog, DialogBody, DialogFooter, DialogHeader, DialogPanel, DialogTitle, LabelSwap, SettingsRow, Spinner } from '@felinic/ui'
import { KeyRound } from 'lucide-vue-next'

defineProps<{
  authorized: boolean
  authorizing: boolean
  disabled?: boolean
  devicePending: boolean
  deviceLogin?: { user_code: string, verification_url: string } | null
  expiresAt?: string
  error?: string
}>()
const emit = defineEmits<{ connect: [], cancel: [] }>()
const { t } = useI18n()
</script>

<template>
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
      :disabled="disabled || authorizing"
      :loading="authorizing"
      loading-mode="manual"
      @click="devicePending ? emit('cancel') : emit('connect')"
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
  <Dialog
    :open="devicePending && !!deviceLogin"
    @update:open="!$event && emit('cancel')"
  >
    <DialogPanel
      width="lg"
      footer
      :aria-describedby="undefined"
    >
      <DialogHeader>
        <DialogTitle>{{ t('bots.agent.chatgptAccount') }}</DialogTitle>
      </DialogHeader>
      <DialogBody>
        <DeviceCodePanel
          v-if="deviceLogin"
          :code="deviceLogin.user_code"
          :verification-uri="deviceLogin.verification_url"
          :expires-at="expiresAt"
          :hint="t('bots.agent.codexDeviceHint')"
          :retry-loading="authorizing"
          :copy-and-open-label="t('deviceCode.copyAndOpen')"
          :retry-label="t('deviceCode.retry')"
          :expired-label="t('deviceCode.codeExpired')"
          :expires-in-label="(time: string) => t('deviceCode.expiresIn', { time })"
          :copy-failed-message="t('deviceCode.copyFailed')"
          @retry="emit('connect')"
        />
      </DialogBody>
      <DialogFooter>
        <Button
          type="button"
          variant="outline"
          @click="emit('cancel')"
        >
          {{ t('common.cancel') }}
        </Button>
      </DialogFooter>
    </DialogPanel>
  </Dialog>
  <p
    v-if="error"
    class="px-4 text-sm text-destructive"
  >
    {{ error }}
  </p>
</template>
