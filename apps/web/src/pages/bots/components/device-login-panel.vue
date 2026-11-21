<script setup lang="ts">
import { computed, onActivated, onDeactivated, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button, DeviceCodePanel, Dialog, DialogBody, DialogFooter, DialogHeader, DialogPanel, DialogTitle, Input, Label, LabelSwap, SettingsRow, Spinner } from '@felinic/ui'
import { ExternalLink, KeyRound } from 'lucide-vue-next'
import type { ExternalagentDeviceLoginAuthorizeResponse } from '@memohai/sdk'
import { AGENT_AUTH_MODE_COPY, DEVICE_LOGINS, type DeviceLoginAuthMode } from '@/utils/bot-agent'

// The account row and sign-in dialog of an Agent's sign-in. The caller drives
// the authorization; the default slot adds row actions. A device code is
// entered on the provider's page; a code_required sign-in (Claude) shows a
// code on that page, which is pasted back here.
const props = defineProps<{
  auth: DeviceLoginAuthMode
  authorized: boolean
  /** The account the Agent is signed in to, when its runtime reports one. */
  account?: string
  /** Shown instead of the account description, e.g. while the sign-in is being checked. */
  note?: string
  authorizing: boolean
  disabled?: boolean
  deviceLogin?: ExternalagentDeviceLoginAuthorizeResponse | null
  /** The pasted code is being checked. */
  submitting?: boolean
}>()
const emit = defineEmits<{ connect: [], cancel: [], submit: [code: string] }>()
const code = ref('')
watch(() => props.deviceLogin, () => { code.value = '' })
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
      <DialogBody
        v-if="deviceLogin?.code_required"
        class="space-y-4"
      >
        <p class="text-body text-muted-foreground">
          {{ t(login.hint) }}
        </p>
        <Button
          as="a"
          variant="outline"
          :href="deviceLogin.verification_url"
          target="_blank"
          rel="noopener noreferrer"
        >
          <ExternalLink />
          {{ t('bots.agent.openAuthorizationPage') }}
        </Button>
        <form
          id="agent-login-code-form"
          class="space-y-2"
          @submit.prevent="code.trim() && emit('submit', code.trim())"
        >
          <Label for="agent-login-code">{{ t('bots.agent.authorizationCode') }}</Label>
          <Input
            id="agent-login-code"
            v-model="code"
            :placeholder="t('bots.agent.authorizationCodePlaceholder')"
            autocomplete="off"
            :spellcheck="false"
            :disabled="submitting"
          />
        </form>
      </DialogBody>
      <DialogBody v-else>
        <DeviceCodePanel
          v-if="deviceLogin?.user_code"
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
        <Button
          v-if="deviceLogin?.code_required"
          type="submit"
          form="agent-login-code-form"
          :disabled="!code.trim() || submitting"
          :loading="submitting"
        >
          {{ t('bots.agent.completeAuthorization') }}
        </Button>
      </DialogFooter>
    </DialogPanel>
  </Dialog>
</template>
