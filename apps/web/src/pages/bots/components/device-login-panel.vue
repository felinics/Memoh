<script setup lang="ts">
import { computed, onActivated, onDeactivated, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button, DeviceCodePanel, Dialog, DialogBody, DialogFooter, DialogHeader, DialogPanel, DialogTitle, LabelSwap, SettingsRow, Spinner } from '@felinic/ui'
import { KeyRound } from 'lucide-vue-next'
import { AGENT_AUTH_MODE_COPY, DEVICE_LOGINS, type DeviceLoginAuthMode } from '@/utils/bot-agent'

// The account row and device-code dialog of an Agent's sign-in. The caller
// drives the authorization; the default slot adds row actions.
const props = defineProps<{
  auth: DeviceLoginAuthMode
  authorized: boolean
  /** The account the Agent is signed in to, when its runtime reports one. */
  account?: string
  /** Shown instead of the account description, e.g. while the sign-in is being checked. */
  note?: string
  authorizing: boolean
  disabled?: boolean
  deviceLogin?: { user_code: string, verification_url: string } | null
}>()
const emit = defineEmits<{ connect: [], cancel: [] }>()
const { t } = useI18n()
const login = computed(() => DEVICE_LOGINS[props.auth])
const label = computed(() => t(AGENT_AUTH_MODE_COPY[props.auth].label))
const devicePending = computed(() => !!props.deviceLogin)
const description = computed(() => {
  if (props.note) return props.note
  if (!props.authorized) return t(login.value.agentDescription)
  return props.account ? t('bots.agent.signedInAs', { account: props.account }) : t(login.value.connectedDescription)
})
// KeepAlive does not take a teleported dialog away with its deactivated host,
// and a sign-in that outlives the deactivation would leave it over the next page.
const active = ref(true)
onActivated(() => { active.value = true })
onDeactivated(() => { active.value = false })
</script>

<template>
  <SettingsRow
    :label="label"
    :description="description"
  >
    <div class="flex gap-2">
      <Button
        type="button"
        variant="outline"
        size="sm"
        class="shrink-0"
        :loading="authorizing"
        :disabled="disabled && !devicePending"
        loading-mode="manual"
        @click="devicePending ? emit('cancel') : emit('connect')"
      >
        <LabelSwap :active="authorizing ? 'connecting' : devicePending ? 'cancel' : 'connect'">
          <template #connect>
            <KeyRound />
            {{ authorized ? t('bots.agent.reconnect') : t('provider.oauth.connect') }}
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
      <slot />
    </div>
  </SettingsRow>
  <!-- The code is approved in another tab, and the click or key that brings
       the user back must not throw that approval away: only Cancel ends it. -->
  <Dialog :open="active && devicePending">
    <DialogPanel
      width="lg"
      footer
      :show-close-button="false"
      :aria-describedby="undefined"
      @escape-key-down.prevent
      @interact-outside.prevent
    >
      <DialogHeader>
        <DialogTitle>{{ label }}</DialogTitle>
      </DialogHeader>
      <DialogBody>
        <DeviceCodePanel
          v-if="deviceLogin"
          :code="deviceLogin.user_code"
          :verification-uri="deviceLogin.verification_url"
          :hint="t(login.hint)"
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
</template>
